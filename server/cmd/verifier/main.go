// Command verifier is the demo backend for "Keys That Never Leave the Chip".
//
//	go run ./cmd/verifier
//	adb reverse tcp:7447 tcp:7447    # the phone reaches it at 127.0.0.1:7447
package main

import (
	"context"
	"encoding/hex"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"strings"
	"time"

	"chipkey/api"
	"chipkey/attest"
)

func main() {
	addr := flag.String("addr", "127.0.0.1:7447", "listen address; use 0.0.0.0:7447 to reach it over Wi-Fi instead of adb reverse")
	pkg := flag.String("package", "dev.chipkey.demo", "Android application ID allowed to enrol; empty accepts any")
	digest := flag.String("cert-digest", "", "hex SHA-256 of the app signing certificate to pin; empty logs it instead")
	threshold := flag.Int64("threshold", 2000, "payments of this many rupees or more need a signed transaction")
	reduced := flag.Int64("reduced-limit", 500, "per-payment cap in rupees for phones without full hardware trust")
	tokenTTL := flag.Duration("token-ttl", 15*time.Minute, "lifetime of an X-Token")
	nonceTTL := flag.Duration("nonce-ttl", 2*time.Minute, "lifetime of a challenge; it has to outlast the PIN screen")
	mode := flag.String("mode", "chip", "which night to start in: chip, or legacy for the app before the chip (OTP only)")
	approvalTTL := flag.Duration("approval-ttl", 20*time.Second, "how long the old phone has to approve a new one before the request dies")
	strict := flag.Bool("strict", false, "refuse phones that cannot prove hardware backing, instead of giving them the software tier")
	extraRoot := flag.String("extra-root", "", "PEM file with an extra trusted root, for rehearsal with cmd/fakephone only")
	revocation := flag.String("revocation", "off", "off, online (fetch Google's list at start), or a path to a saved copy of it")
	dashUser := flag.String("dash-user", envOr("CHIPKEY_DASH_USER", "admin"), "dashboard login name; default from $CHIPKEY_DASH_USER")
	dashPass := flag.String("dash-pass", os.Getenv("CHIPKEY_DASH_PASS"), "dashboard password; default from $CHIPKEY_DASH_PASS. Empty leaves the dashboard open")
	noColor := flag.Bool("no-color", false, "plain log lines")
	flag.Parse()

	roots, err := attest.GoogleRoots()
	if err != nil {
		log.Fatalf("embedded Google roots: %v", err)
	}
	trust := fmt.Sprintf("%d Google hardware attestation roots", len(roots))
	if *extraRoot != "" {
		r, err := attest.RootFromFile("a FAKE REHEARSAL ROOT", *extraRoot)
		if err != nil {
			log.Fatalf("-extra-root: %v", err)
		}
		roots = append(roots, r)
		trust += " + FAKE REHEARSAL ROOT (not for real phones)"
	}
	opts := attest.Options{Roots: roots}

	revoked := "not checked"
	switch *revocation {
	case "off":
	case "online":
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		list, err := attest.FetchStatusList(ctx, attest.StatusURL)
		cancel()
		if err != nil {
			log.Fatalf("-revocation online: %v", err)
		}
		opts.Revoked, revoked = list.Revoked, fmt.Sprintf("%d revoked serials from Google", list.Len())
	default:
		list, err := attest.LoadStatusList(*revocation)
		if err != nil {
			log.Fatalf("-revocation %s: %v", *revocation, err)
		}
		opts.Revoked, revoked = list.Revoked, fmt.Sprintf("%d revoked serials from %s", list.Len(), *revocation)
	}

	cfg := api.Config{
		Package: *pkg, AllowSoftware: !*strict,
		Threshold: *threshold, ReducedLimit: *reduced,
		TokenTTL: *tokenTTL, NonceTTL: *nonceTTL,
		Mode: *mode, ApprovalTTL: *approvalTTL,
		DashUser: *dashUser, DashPass: *dashPass,
		Attest: opts, Out: os.Stdout, Color: !*noColor,
	}
	if *digest != "" {
		b, err := hex.DecodeString(strings.ReplaceAll(*digest, ":", ""))
		if err != nil {
			log.Fatalf("-cert-digest: %v", err)
		}
		cfg.CertDigest = b
	}

	fmt.Printf("chipkey verifier listening on http://%s\n", *addr)
	fmt.Printf("  trust        %s\n", trust)
	fmt.Printf("  revocation   %s\n", revoked)
	fmt.Printf("  app          %s\n", orAny(*pkg))
	fmt.Printf("  step-up at   %s    reduced limit %s\n", api.Rupees(*threshold), api.Rupees(*reduced))
	fmt.Printf("  X-Token      valid %s    challenge valid %s\n", *tokenTTL, *nonceTTL)
	fmt.Printf("  night        %s mode    new-device approval window %s\n", *mode, *approvalTTL)
	fmt.Printf("  dashboard    http://127.0.0.1:%s/   keys: L old app, C chip, R rewind\n", port(*addr))
	if *dashPass != "" {
		fmt.Printf("  login        dashboard and stage controls need a sign-in as %s\n", *dashUser)
	} else {
		fmt.Printf("  login        OFF: the dashboard and stage controls are open to anyone who can reach this port\n")
	}
	fmt.Printf("  phone setup  adb reverse tcp:%s tcp:%s\n\n", port(*addr), port(*addr))

	srv := &http.Server{Addr: *addr, Handler: api.New(cfg), ReadHeaderTimeout: 5 * time.Second}
	log.Fatal(srv.ListenAndServe())
}

func orAny(s string) string {
	if s == "" {
		return "any package"
	}
	return s
}

func port(addr string) string {
	if i := strings.LastIndex(addr, ":"); i >= 0 {
		return addr[i+1:]
	}
	return addr
}

// envOr reads a setting from the environment, so a password never has to be
// written in the source or on a command line that other users can list.
func envOr(name, fallback string) string {
	if v := os.Getenv(name); v != "" {
		return v
	}
	return fallback
}

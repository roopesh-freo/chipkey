// Command fakechip plays the secure hardware for the Dart end-to-end test.
// The Flutter test suite cannot reach a real Keystore, so it shells out here
// for the two things only a chip can do: attest a new key and sign with it.
//
//	fakechip -ca DIR init
//	fakechip -ca DIR -state FILE enrol <challenge-base64>   -> JSON {chain, keyId, tier}
//	fakechip -state FILE sign <payload-base64>              -> signature, base64
package main

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"os"

	"chipkey/internal/testchain"
)

func main() {
	ca := flag.String("ca", ".fakephone", "directory holding the fake attestation root")
	state := flag.String("state", "fakechip.key", "file holding this fake chip's private key")
	pkg := flag.String("package", "dev.chipkey.demo", "application ID in the attestation")
	flag.Parse()
	args := flag.Args()
	if len(args) == 0 {
		die(fmt.Errorf("usage: fakechip init | enrol <challenge> | sign <payload>"))
	}

	switch args[0] {
	case "init":
		_, err := testchain.LoadOrCreate(*ca)
		die(err)

	case "enrol":
		challenge, err := base64.StdEncoding.DecodeString(args[1])
		die(err)
		authority, err := testchain.LoadOrCreate(*ca)
		die(err)
		key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
		die(err)
		chain, err := authority.Issue(&key.PublicKey, testchain.Options{
			Challenge: challenge, AttestationLevel: 2, KeyLevel: 2,
			DeviceLocked: true, BootState: 0,
			Package: *pkg, SignatureDigest: make([]byte, 32),
		})
		die(err)
		der, err := x509.MarshalECPrivateKey(key)
		die(err)
		die(os.WriteFile(*state, der, 0o600))

		spki, err := x509.MarshalPKIXPublicKey(&key.PublicKey)
		die(err)
		id := sha256.Sum256(spki)
		out := struct {
			Chain []string `json:"chain"`
			KeyID string   `json:"keyId"`
			Tier  string   `json:"tier"`
		}{KeyID: hex.EncodeToString(id[:8]), Tier: "strongbox"}
		for _, c := range chain {
			out.Chain = append(out.Chain, base64.StdEncoding.EncodeToString(c))
		}
		die(json.NewEncoder(os.Stdout).Encode(out))

	case "sign":
		payload, err := base64.StdEncoding.DecodeString(args[1])
		die(err)
		der, err := os.ReadFile(*state)
		die(err)
		key, err := x509.ParseECPrivateKey(der)
		die(err)
		digest := sha256.Sum256(payload)
		sig, err := ecdsa.SignASN1(rand.Reader, key, digest[:])
		die(err)
		fmt.Println(base64.StdEncoding.EncodeToString(sig))

	default:
		die(fmt.Errorf("unknown command %q", args[0]))
	}
}

func die(err error) {
	if err != nil {
		fmt.Fprintln(os.Stderr, "fakechip:", err)
		os.Exit(1)
	}
}

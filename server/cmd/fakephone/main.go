// Command fakephone plays both nights of the talk against a running
// verifier, without any device. It stands in for two handsets: Meera's
// phone and the thief's. Keys are software and the attestation root is fake.
//
//	go run ./cmd/fakephone -init                      # writes .fakephone/root.pem
//	go run ./cmd/verifier -extra-root .fakephone/root.pem
//	go run ./cmd/fakephone                            # the night as it happened, then again with the chip
//	go run ./cmd/fakephone -night chip                # only the second night
package main

import (
	"bytes"
	"encoding/base64"
	"flag"
	"fmt"
	"os"
	"time"

	"chipkey/internal/phone"
	"chipkey/internal/testchain"
)

const number = "9876543210"

var pause time.Duration

func step(format string, args ...any) {
	time.Sleep(pause)
	fmt.Printf("\n▶ "+format+"\n", args...)
}

func check(err error) {
	if err != nil {
		fmt.Fprintln(os.Stderr, "fakephone:", err)
		os.Exit(1)
	}
}

func main() {
	base := flag.String("url", "http://127.0.0.1:7447", "verifier base URL")
	dir := flag.String("ca", ".fakephone", "directory holding the fake attestation root")
	initOnly := flag.Bool("init", false, "create the fake root and exit")
	night := flag.String("night", "both", "which night to play: happened, chip or both")
	flag.DurationVar(&pause, "pause", 900*time.Millisecond, "delay between steps, so the dashboard is readable")
	flag.Parse()

	ca, err := testchain.LoadOrCreate(*dir)
	check(err)
	if *initOnly {
		fmt.Printf("fake root ready: %s/root.pem\nstart the verifier with: go run ./cmd/verifier -extra-root %s/root.pem\n", *dir, *dir)
		return
	}

	handset := func(name string, level int) *phone.Phone {
		p := phone.New(*base, ca, testchain.Options{
			AttestationLevel: level, KeyLevel: level, DeviceLocked: true,
			Package: "dev.chipkey.demo", SignatureDigest: bytes.Repeat([]byte{0xFA}, 32),
		})
		p.Account, p.Name = number, name
		return p
	}

	if *night == "happened" || *night == "both" {
		asItHappened(handset("Meera's phone", 1), handset("The thief's phone", 2))
	}
	if *night == "chip" || *night == "both" {
		withTheChip(handset("Meera's phone", 1), handset("The thief's phone", 2))
	}
}

func asItHappened(meera, thief *phone.Phone) {
	step("6:40 PM  the old app. A phone number is proven by an OTP")
	check(meera.Post("/v1/demo", map[string]any{"reset": true, "mode": "legacy"}, nil))

	step("Meera logs in the way she always has")
	_, err := meera.LoginStart()
	check(err)
	code, err := meera.OTP(number, false)
	check(err)
	_, err = meera.LoginOTP(code)
	check(err)

	step("11:52 PM  he types her number into his own phone")
	_, err = thief.LoginStart()
	check(err)

	step("11:54 PM  the OTP reaches her phone, and is forwarded")
	code, err = thief.OTP(number, true)
	check(err)
	fmt.Printf("  forwarded to him: %s\n", code)
	_, err = thief.LoginOTP(code)
	check(err)

	for _, at := range []string{"2:07", "2:09", "2:11"} {
		step("%s AM  ₹25,000 to evil@upi", at)
		_, err = thief.Pay(25000, "evil@upi")
		check(err)
	}
	bal, _ := meera.Balance()
	fmt.Printf("\n  7:00 AM  Meera wakes up. Balance ₹%d\n", bal)
}

func withTheChip(meera, thief *phone.Phone) {
	step("REWIND  same night, same message, same thief. One change")
	check(meera.Post("/v1/demo", map[string]any{"reset": true, "mode": "chip"}, nil))

	step("Meera's phone is bound once: a key in its chip, proved by attestation")
	st, err := meera.LoginStart()
	check(err)
	_, err = meera.Bind(st)
	check(err)
	st, err = meera.LoginStart()
	check(err)
	check(meera.SignIn(st.Nonce))

	step("11:52 PM again  he types her number into his own phone")
	_, err = thief.LoginStart()
	check(err)

	step("11:54 PM again  the OTP is still forwarded")
	code, err := thief.OTP(number, true)
	check(err)
	st, err = thief.LoginOTP(code)
	check(err)

	step("his phone is real, his chip is real, his attestation passes")
	verdict, err := thief.Bind(st)
	check(err)
	id, _ := verdict["approvalId"].(string)
	fmt.Printf("  backend says: %v. Her phone is being asked\n", verdict["status"])

	step("she is asleep. Nobody approves")
	for {
		status, err := thief.ApprovalStatus(id)
		check(err)
		if status != "pending" {
			fmt.Printf("  his phone: %s\n", status)
			break
		}
		time.Sleep(time.Second)
	}

	step("his last move: “I lost my phone”")
	var out map[string]any
	check(thief.Post("/v1/recovery/start", map[string]any{"phone": number, "deviceId": thief.DeviceID}, &out))
	fmt.Printf("  backend says: %v\n", out["status"])

	step("9:10 AM  plan B: Meera pays ₹25,000 to a shop, and he rewrites the payee")
	su, err := meera.Pay(25000, "shop@upi")
	check(err)
	honest := meera.PayPayload(su.TxnID, su.Nonce, 25000, "shop@upi")
	payload, sig := meera.Sign(honest)
	forged := bytes.Replace(honest, []byte("shop@upi"), []byte("evil@upi"), 1)
	fmt.Printf("  %v\n", meera.Confirm(su.TxnID, base64.StdEncoding.EncodeToString(forged), sig))

	step("the payment she actually approved goes through")
	check(meera.Confirm(su.TxnID, payload, sig))
	bal, _ := meera.Balance()
	fmt.Printf("\n  7:00 AM  Meera wakes up. Balance ₹%d\n", bal)
}

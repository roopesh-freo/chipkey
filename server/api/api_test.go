package api_test

import (
	"bytes"
	"encoding/base64"
	"errors"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"chipkey/api"
	"chipkey/attest"
	"chipkey/internal/phone"
	"chipkey/internal/testchain"
)

type world struct {
	t   *testing.T
	ca  *testchain.Authority
	log *bytes.Buffer
	now time.Time
	url string
}

// start runs a backend that trusts a fresh fake root. tweak adjusts policy.
func start(t *testing.T, tweak func(*api.Config)) *world {
	t.Helper()
	ca, err := testchain.New()
	if err != nil {
		t.Fatal(err)
	}
	w := &world{t: t, ca: ca, log: &bytes.Buffer{}, now: time.Now()}
	cfg := api.Config{
		Package: "dev.chipkey.demo", AllowSoftware: true,
		Threshold: 2000, ReducedLimit: 500,
		Attest: attest.Options{Roots: []attest.Root{{Name: "test-root", SPKI: ca.RootCert.RawSubjectPublicKeyInfo}}},
		Out:    w.log,
		Now:    func() time.Time { return w.now },
	}
	if tweak != nil {
		tweak(&cfg)
	}
	srv := httptest.NewServer(api.New(cfg))
	t.Cleanup(srv.Close)
	w.url = srv.URL
	return w
}

func chip(level int) testchain.Options {
	return testchain.Options{
		AttestationLevel: level, KeyLevel: level, DeviceLocked: true, BootState: 0,
		Package: "dev.chipkey.demo", SignatureDigest: []byte{1, 2, 3},
	}
}

func (w *world) phone(o testchain.Options) *phone.Phone {
	p := phone.New(w.url, w.ca, o)
	p.Now = func() time.Time { return w.now }
	return p
}

func wantCode(t *testing.T, err error, code string) {
	t.Helper()
	var e *phone.APIError
	if !errors.As(err, &e) || e.Code != code {
		t.Fatalf("got %v, want API error %q", err, code)
	}
}

func TestThreeActs(t *testing.T) {
	w := start(t, nil)
	p := w.phone(chip(2))

	verdict, err := p.Enrol()
	if err != nil {
		t.Fatalf("enrol: %v", err)
	}
	if verdict["tier"] != "strongbox" || verdict["fullLimits"] != true {
		t.Fatalf("verdict = %v", verdict)
	}

	if _, err := p.Balance(); err == nil {
		t.Fatal("balance served without a session")
	}
	if err := p.Login(); err != nil {
		t.Fatalf("login: %v", err)
	}
	if bal, err := p.Balance(); err != nil || bal != 100000 {
		t.Fatalf("balance = %d, %v", bal, err)
	}

	// Under the threshold: the token is enough.
	if su, err := p.Pay(500, "chai@upi"); err != nil || su != nil {
		t.Fatalf("small payment: %v %v", su, err)
	}

	// At the threshold: the backend wants the chip.
	su, err := p.Pay(2000, "shop@upi")
	if err != nil || su == nil {
		t.Fatalf("large payment did not ask for step-up: %v", err)
	}
	payload, sig := p.Sign(p.PayPayload(su.TxnID, su.Nonce, 2000, "shop@upi"))
	if err := p.Confirm(su.TxnID, payload, sig); err != nil {
		t.Fatalf("confirm: %v", err)
	}
	if bal, _ := p.Balance(); bal != 100000-500-2000 {
		t.Fatalf("balance after payments = %d", bal)
	}

	// The same signed approval cannot pay twice.
	wantCode(t, p.Confirm(su.TxnID, payload, sig), "txn_unknown")

	out := w.log.String()
	for _, want := range []string{"tier=STRONGBOX", "X-Token", "step-up required", "₹2,000 to shop@upi. Paid."} {
		if !strings.Contains(out, want) {
			t.Errorf("terminal log is missing %q:\n%s", want, out)
		}
	}
}

func TestTamperedPaymentIsRejected(t *testing.T) {
	w := start(t, nil)
	p := w.phone(chip(1))
	if _, err := p.Enrol(); err != nil {
		t.Fatal(err)
	}
	if err := p.Login(); err != nil {
		t.Fatal(err)
	}
	su, err := p.Pay(25000, "shop@upi")
	if err != nil || su == nil {
		t.Fatal(err)
	}
	honest := p.PayPayload(su.TxnID, su.Nonce, 25000, "shop@upi")
	_, sig := p.Sign(honest)

	// Someone in the middle rewrites the payee after the chip signed.
	forged := bytes.Replace(honest, []byte("shop@upi"), []byte("evil@upi"), 1)
	wantCode(t, p.Confirm(su.TxnID, base64.StdEncoding.EncodeToString(forged), sig), "bad_signature")
	if !strings.Contains(w.log.String(), "signature INVALID") {
		t.Errorf("terminal did not announce the invalid signature:\n%s", w.log)
	}

	// A correctly signed approval for a different amount is also refused.
	otherPayload, otherSig := p.Sign(p.PayPayload(su.TxnID, su.Nonce, 1, "shop@upi"))
	wantCode(t, p.Confirm(su.TxnID, otherPayload, otherSig), "payload_mismatch")

	// The untouched approval still goes through.
	if err := p.Confirm(su.TxnID, base64.StdEncoding.EncodeToString(honest), sig); err != nil {
		t.Fatalf("honest confirm after failed attempts: %v", err)
	}
	if bal, _ := p.Balance(); bal != 75000 {
		t.Fatalf("balance = %d", bal)
	}
}

func TestLoginSignatureCannotApproveAPayment(t *testing.T) {
	w := start(t, nil)
	p := w.phone(chip(2))
	p.Enrol()
	if err := p.Login(); err != nil {
		t.Fatal(err)
	}
	su, _ := p.Pay(5000, "shop@upi")

	// Replay protection for sessions: a signed login works once.
	nonce, _ := p.SessionNonce()
	payload, sig := p.Sign(p.LoginPayload(nonce))
	if err := p.SubmitLogin(payload, sig); err != nil {
		t.Fatal(err)
	}
	wantCode(t, p.SubmitLogin(payload, sig), "bad_signature")

	// A signature made for login is useless at the payment endpoint.
	nonce, _ = p.SessionNonce()
	payload, sig = p.Sign(p.LoginPayload(nonce))
	wantCode(t, p.Confirm(su.TxnID, payload, sig), "payload_mismatch")
}

func TestSessionRejectsOtherKeysAndStaleClocks(t *testing.T) {
	w := start(t, nil)
	p := w.phone(chip(2))
	p.Enrol()

	thief := w.phone(chip(2))
	thief.Attest([]byte("x")) // a different private key
	thief.DeviceID, thief.KeyID = p.DeviceID, p.KeyID
	nonce, _ := p.SessionNonce()
	payload, sig := thief.Sign(thief.LoginPayload(nonce))
	wantCode(t, thief.SubmitLogin(payload, sig), "bad_signature")

	// Signed ten minutes ago.
	nonce, _ = p.SessionNonce()
	p.Now = func() time.Time { return w.now.Add(-10 * time.Minute) }
	payload, sig = p.Sign(p.LoginPayload(nonce))
	wantCode(t, p.SubmitLogin(payload, sig), "bad_signature")

	// Nonce left to expire.
	p.Now = func() time.Time { return w.now }
	nonce, _ = p.SessionNonce()
	payload, sig = p.Sign(p.LoginPayload(nonce))
	w.now = w.now.Add(2 * time.Minute)
	wantCode(t, p.SubmitLogin(payload, sig), "bad_signature")
}

func TestTokenExpires(t *testing.T) {
	w := start(t, nil)
	p := w.phone(chip(2))
	p.Enrol()
	if err := p.Login(); err != nil {
		t.Fatal(err)
	}
	w.now = w.now.Add(16 * time.Minute)
	_, err := p.Balance()
	wantCode(t, err, "token_invalid")
	_, err = p.Pay(100, "chai@upi")
	wantCode(t, err, "token_invalid")
}

func TestEnrolChecksChallengeAppAndAuthBinding(t *testing.T) {
	w := start(t, nil)

	// A chain attesting a challenge the backend never issued.
	p := w.phone(chip(2))
	chain, _ := p.Attest([]byte("made-up-challenge"))
	_, err := p.Submit(chain)
	wantCode(t, err, "challenge_mismatch")

	// A genuine chain cannot be replayed, by this device or another.
	challenge, _ := p.Challenge()
	chain, _ = p.Attest(challenge)
	if _, err := p.Submit(chain); err != nil {
		t.Fatal(err)
	}
	_, err = p.Submit(chain)
	wantCode(t, err, "challenge_mismatch")

	other := w.phone(chip(2))
	challenge, _ = p.Challenge() // issued to p
	chain, _ = other.Attest(challenge)
	_, err = other.Submit(chain)
	wantCode(t, err, "challenge_mismatch")

	// A key requested by some other app.
	wrong := chip(2)
	wrong.Package = "com.evil.clone"
	_, err = w.phone(wrong).Enrol()
	wantCode(t, err, "wrong_app")

	// A hardware key that needs no biometrics.
	open := chip(2)
	open.NoAuthRequired = true
	_, err = w.phone(open).Enrol()
	wantCode(t, err, "key_not_auth_bound")
}

func TestSigningCertificatePin(t *testing.T) {
	w := start(t, func(c *api.Config) { c.CertDigest = []byte{9, 9, 9} })
	_, err := w.phone(chip(2)).Enrol()
	wantCode(t, err, "wrong_signer")

	w = start(t, func(c *api.Config) { c.CertDigest = []byte{1, 2, 3} })
	if _, err := w.phone(chip(2)).Enrol(); err != nil {
		t.Fatalf("matching digest refused: %v", err)
	}
}

func TestUnlockedBootloaderGetsReducedLimits(t *testing.T) {
	w := start(t, nil)
	rooted := chip(1)
	rooted.DeviceLocked, rooted.BootState = false, 2
	p := w.phone(rooted)
	verdict, err := p.Enrol()
	if err != nil {
		t.Fatal(err)
	}
	if verdict["tier"] != "tee" || verdict["integrity"] != false || verdict["fullLimits"] != false {
		t.Fatalf("verdict = %v", verdict)
	}
	p.Login()
	if su, err := p.Pay(400, "chai@upi"); err != nil || su != nil {
		t.Fatalf("small payment on reduced limits: %v %v", su, err)
	}
	_, err = p.Pay(5000, "shop@upi")
	wantCode(t, err, "limit_exceeded")
}

func TestUntrustedRootIsSoftwareTierOrRefused(t *testing.T) {
	// The phone's chain hangs off a root this backend does not trust.
	stranger, _ := testchain.New()

	w := start(t, nil)
	p := phone.New(w.url, stranger, chip(2)) // claims StrongBox
	verdict, err := p.Enrol()
	if err != nil {
		t.Fatal(err)
	}
	if verdict["tier"] != "software" || verdict["fullLimits"] != false {
		t.Fatalf("unverifiable phone was believed: %v", verdict)
	}
	p.Login()
	_, err = p.Pay(5000, "shop@upi")
	wantCode(t, err, "limit_exceeded")

	strict := start(t, func(c *api.Config) { c.AllowSoftware = false })
	_, err = phone.New(strict.url, stranger, chip(2)).Enrol()
	wantCode(t, err, "attestation_rejected")
}

func TestReEnrolDropsOldSessions(t *testing.T) {
	w := start(t, nil)
	p := w.phone(chip(2))
	p.Enrol()
	p.Login()
	old := p.Token
	if _, err := p.Enrol(); err != nil {
		t.Fatal(err)
	}
	p.Token = old
	_, err := p.Balance()
	wantCode(t, err, "token_invalid")
}

func TestRupees(t *testing.T) {
	for in, want := range map[int64]string{5: "₹5", 500: "₹500", 2000: "₹2,000", 25000: "₹25,000", 100000: "₹1,00,000", 12345678: "₹1,23,45,678"} {
		if got := api.Rupees(in); got != want {
			t.Errorf("Rupees(%d) = %q, want %q", in, got, want)
		}
	}
}

func TestNothingIsPaidBeforeConfirm(t *testing.T) {
	w := start(t, nil)
	p := w.phone(chip(2))
	p.Enrol()
	p.Login()

	// A small payment waits for the app to confirm, for example after a PIN.
	small, su, err := p.Start(500, "chai@upi")
	if err != nil || su != nil {
		t.Fatalf("small payment: %v %v", su, err)
	}
	if bal, _ := p.Balance(); bal != 100000 {
		t.Fatalf("balance moved before confirm: %d", bal)
	}
	if err := p.Confirm(small, "", ""); err != nil {
		t.Fatal(err)
	}
	if bal, _ := p.Balance(); bal != 99500 {
		t.Fatalf("balance after confirm: %d", bal)
	}
	wantCode(t, p.Confirm(small, "", ""), "txn_unknown")

	// A large payment cannot take the small payment's shortcut.
	large, su, err := p.Start(25000, "shop@upi")
	if err != nil || su == nil {
		t.Fatalf("large payment did not ask for step-up: %v", err)
	}
	if err := p.Confirm(large, "", ""); err == nil {
		t.Fatal("step-up payment confirmed without a signature")
	}
	if bal, _ := p.Balance(); bal != 99500 {
		t.Fatalf("unsigned confirm moved money: %d", bal)
	}
}

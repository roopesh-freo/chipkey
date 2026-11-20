package attest_test

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"errors"
	"math/big"
	"strings"
	"testing"
	"time"

	"chipkey/attest"
	"chipkey/internal/testchain"
)

func newKey(t *testing.T) *ecdsa.PrivateKey {
	t.Helper()
	k, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return k
}

func setup(t *testing.T) (*testchain.Authority, attest.Options) {
	t.Helper()
	a, err := testchain.New()
	if err != nil {
		t.Fatal(err)
	}
	return a, attest.Options{Roots: []attest.Root{{Name: "test", SPKI: a.RootCert.RawSubjectPublicKeyInfo}}}
}

func good(challenge string) testchain.Options {
	return testchain.Options{
		Challenge: []byte(challenge), AttestationLevel: 2, KeyLevel: 2,
		DeviceLocked: true, BootState: 0,
		Package: "dev.chipkey.demo", SignatureDigest: []byte{0xAA, 0xBB},
	}
}

func TestVerifyStrongBox(t *testing.T) {
	a, opts := setup(t)
	key := newKey(t)
	chain, err := a.Issue(&key.PublicKey, good("nonce-1"))
	if err != nil {
		t.Fatal(err)
	}
	res, err := attest.Verify(chain, opts)
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if string(res.Challenge) != "nonce-1" {
		t.Errorf("challenge = %q", res.Challenge)
	}
	if res.KeyLevel != attest.StrongBox || res.AttestationLevel != attest.StrongBox {
		t.Errorf("levels = %v / %v", res.AttestationLevel, res.KeyLevel)
	}
	if !res.Integrity() {
		t.Errorf("integrity false: %+v", res)
	}
	if !res.UserAuthRequired || res.UserAuthType != 2 {
		t.Errorf("user auth = %v type %d", res.UserAuthRequired, res.UserAuthType)
	}
	if len(res.PackageNames) != 1 || res.PackageNames[0] != "dev.chipkey.demo" {
		t.Errorf("packages = %v", res.PackageNames)
	}
	if len(res.SignatureDigests) != 1 || res.SignatureDigests[0][0] != 0xAA {
		t.Errorf("digests = %x", res.SignatureDigests)
	}
	if !res.PublicKey.Equal(&key.PublicKey) {
		t.Error("public key does not match the attested key")
	}
	if res.RootName != "test" || res.ChainLen != 3 {
		t.Errorf("root %q len %d", res.RootName, res.ChainLen)
	}
}

func TestVerifyReportsDeviceState(t *testing.T) {
	a, opts := setup(t)
	key := newKey(t)

	o := good("n")
	o.KeyLevel, o.AttestationLevel = 1, 1
	o.DeviceLocked, o.BootState = false, 2
	chain, _ := a.Issue(&key.PublicKey, o)
	res, err := attest.Verify(chain, opts)
	if err != nil {
		t.Fatal(err)
	}
	if res.KeyLevel != attest.TrustedEnvironment {
		t.Errorf("key level = %v", res.KeyLevel)
	}
	if res.Integrity() || res.DeviceLocked || res.BootState != attest.Unverified {
		t.Errorf("unlocked phone reported as intact: %+v", res)
	}

	o = good("n")
	o.NoAuthRequired = true
	chain, _ = a.Issue(&key.PublicKey, o)
	res, err = attest.Verify(chain, opts)
	if err != nil {
		t.Fatal(err)
	}
	if res.UserAuthRequired {
		t.Error("key without user auth reported as auth-bound")
	}

	o = good("n")
	o.OmitRootOfTrust = true
	chain, _ = a.Issue(&key.PublicKey, o)
	res, err = attest.Verify(chain, opts)
	if err != nil {
		t.Fatal(err)
	}
	if res.HasRootOfTrust || res.Integrity() {
		t.Error("missing root of trust reported as intact")
	}
}

func TestRejectsUntrustedRoot(t *testing.T) {
	a, _ := setup(t)
	other, opts := setup(t)
	_ = other
	key := newKey(t)
	chain, _ := a.Issue(&key.PublicKey, good("n"))
	_, err := attest.Verify(chain, opts)
	if !errors.Is(err, attest.ErrUntrustedRoot) {
		t.Fatalf("err = %v, want ErrUntrustedRoot", err)
	}
	// The lowest tier can still read the key, without believing it.
	res, err := attest.DescribeUnverified(chain)
	if err != nil || !res.PublicKey.Equal(&key.PublicKey) || !res.HasAttestation {
		t.Fatalf("DescribeUnverified: %v %+v", err, res)
	}
}

func TestRejectsGoogleRootsForFakeChain(t *testing.T) {
	a, _ := setup(t)
	roots, err := attest.GoogleRoots()
	if err != nil {
		t.Fatal(err)
	}
	if len(roots) != 2 {
		t.Fatalf("embedded %d Google roots, want 2", len(roots))
	}
	key := newKey(t)
	chain, _ := a.Issue(&key.PublicKey, good("n"))
	if _, err := attest.Verify(chain, attest.Options{Roots: roots}); !errors.Is(err, attest.ErrUntrustedRoot) {
		t.Fatalf("fake chain accepted against Google roots: %v", err)
	}
}

func TestRejectsTamperedChain(t *testing.T) {
	a, opts := setup(t)
	key := newKey(t)
	chain, _ := a.Issue(&key.PublicKey, good("n"))

	// Swap in a leaf signed by a different batch key.
	b, _ := testchain.New()
	otherChain, _ := b.Issue(&key.PublicKey, good("n"))
	forged := [][]byte{otherChain[0], chain[1], chain[2]}
	if _, err := attest.Verify(forged, opts); err == nil || !strings.Contains(err.Error(), "not signed by") {
		t.Fatalf("leaf from another authority accepted: %v", err)
	}

	// Flip one byte in the leaf.
	bad := append([]byte(nil), chain[0]...)
	bad[len(bad)-5] ^= 0x01
	if _, err := attest.Verify([][]byte{bad, chain[1], chain[2]}, opts); err == nil {
		t.Fatal("corrupted leaf accepted")
	}

	if _, err := attest.Verify(chain[:1], opts); err == nil {
		t.Fatal("single certificate accepted")
	}
}

// An attacker with one genuine attested key on a rooted phone signs a forged
// leaf for a software key, claiming a locked, verified device.
func TestRejectsExtendedChain(t *testing.T) {
	a, opts := setup(t)
	hwKey := newKey(t)
	honest := good("n")
	honest.DeviceLocked, honest.BootState = false, 2
	chain, _ := a.Issue(&hwKey.PublicKey, honest)
	hwCert, err := x509.ParseCertificate(chain[0])
	if err != nil {
		t.Fatal(err)
	}

	swKey := newKey(t)
	forgedLeaf, err := testchain.SignLeaf(&swKey.PublicKey, hwCert, hwKey, testchain.KeyDescription(good("n")))
	if err != nil {
		t.Fatal(err)
	}
	extended := append([][]byte{forgedLeaf}, chain...)
	_, err = attest.Verify(extended, opts)
	if err == nil || !strings.Contains(err.Error(), "chain was extended") {
		t.Fatalf("extended chain accepted: %v", err)
	}
}

func TestRejectsRevokedAndExpired(t *testing.T) {
	a, opts := setup(t)
	key := newKey(t)
	chain, _ := a.Issue(&key.PublicKey, good("n"))

	revoked := opts
	revoked.Revoked = func(s *big.Int) (string, bool) {
		return "REVOKED KEY_COMPROMISE", s.Cmp(a.IntCert.SerialNumber) == 0
	}
	if _, err := attest.Verify(chain, revoked); err == nil || !strings.Contains(err.Error(), "revoked") {
		t.Fatalf("revoked batch certificate accepted: %v", err)
	}

	late := opts
	late.Now = time.Now().AddDate(2, 0, 0)
	if _, err := attest.Verify(chain, late); err == nil || !strings.Contains(err.Error(), "validity") {
		t.Fatalf("expired batch certificate accepted: %v", err)
	}
}

func TestStatusListLookup(t *testing.T) {
	list, err := attest.ParseStatusList(strings.NewReader(`{"entries":{
		"c35747a084470c3135aeefe2b8d40cd6":{"status":"REVOKED","reason":"KEY_COMPROMISE"},
		"6681152659205225093":{"status":"REVOKED","reason":"SOFTWARE_FLAW"}}}`))
	if err != nil {
		t.Fatal(err)
	}
	hex, _ := new(big.Int).SetString("c35747a084470c3135aeefe2b8d40cd6", 16)
	dec, _ := new(big.Int).SetString("6681152659205225093", 10)
	if _, bad := list.Revoked(hex); !bad {
		t.Error("hex-keyed serial not found")
	}
	if reason, bad := list.Revoked(dec); !bad || !strings.Contains(reason, "SOFTWARE_FLAW") {
		t.Errorf("decimal-keyed serial: %q %v", reason, bad)
	}
	if _, bad := list.Revoked(big.NewInt(12345)); bad {
		t.Error("unlisted serial reported revoked")
	}
}

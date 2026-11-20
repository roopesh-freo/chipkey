// Package testchain builds synthetic Android Key Attestation chains.
//
// It stands in for a phone in tests and in the fakephone rehearsal tool. The
// chains are structurally the same as real ones but hang off a private test
// root, so the verifier only accepts them when that root is added on purpose.
package testchain

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/asn1"
	"encoding/pem"
	"math/big"
	"os"
	"path/filepath"
	"time"
)

var oidKeyDescription = asn1.ObjectIdentifier{1, 3, 6, 1, 4, 1, 11129, 2, 1, 17}

// Authority is a fake attestation root plus the factory key under it.
type Authority struct {
	RootKey  *ecdsa.PrivateKey
	RootCert *x509.Certificate
	IntKey   *ecdsa.PrivateKey
	IntCert  *x509.Certificate
}

// Options describe what the fake chip attests to.
type Options struct {
	Challenge        []byte
	AttestationLevel int // 0 software, 1 TEE, 2 StrongBox
	KeyLevel         int
	DeviceLocked     bool
	BootState        int // 0 verified, 1 self-signed, 2 unverified, 3 failed
	OmitRootOfTrust  bool
	NoAuthRequired   bool
	Package          string
	SignatureDigest  []byte
}

func serial() *big.Int {
	n, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 120))
	if err != nil {
		panic(err)
	}
	return n.Add(n, big.NewInt(1))
}

// New creates a fresh authority.
func New() (*Authority, error) {
	rootKey, err := ecdsa.GenerateKey(elliptic.P384(), rand.Reader)
	if err != nil {
		return nil, err
	}
	now := time.Now()
	rootTmpl := &x509.Certificate{
		SerialNumber:          serial(),
		Subject:               pkix.Name{CommonName: "FAKE Key Attestation Root (chipkey demo)"},
		NotBefore:             now.Add(-time.Hour),
		NotAfter:              now.AddDate(5, 0, 0),
		IsCA:                  true,
		BasicConstraintsValid: true,
		KeyUsage:              x509.KeyUsageCertSign,
	}
	rootDER, err := x509.CreateCertificate(rand.Reader, rootTmpl, rootTmpl, &rootKey.PublicKey, rootKey)
	if err != nil {
		return nil, err
	}
	rootCert, err := x509.ParseCertificate(rootDER)
	if err != nil {
		return nil, err
	}
	a := &Authority{RootKey: rootKey, RootCert: rootCert}
	return a, a.newIntermediate()
}

func (a *Authority) newIntermediate() error {
	intKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return err
	}
	now := time.Now()
	tmpl := &x509.Certificate{
		SerialNumber:          serial(),
		Subject:               pkix.Name{CommonName: "FAKE attestation batch key"},
		NotBefore:             now.Add(-time.Hour),
		NotAfter:              now.AddDate(1, 0, 0),
		IsCA:                  true,
		BasicConstraintsValid: true,
		KeyUsage:              x509.KeyUsageCertSign,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, a.RootCert, &intKey.PublicKey, a.RootKey)
	if err != nil {
		return err
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		return err
	}
	a.IntKey, a.IntCert = intKey, cert
	return nil
}

// RootPEM is the fake root as a PEM certificate.
func (a *Authority) RootPEM() []byte {
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: a.RootCert.Raw})
}

// LoadOrCreate keeps one authority in dir across runs, so a verifier started
// with the saved root keeps trusting the same fake phone.
func LoadOrCreate(dir string) (*Authority, error) {
	keyFile, certFile := filepath.Join(dir, "root-key.der"), filepath.Join(dir, "root.pem")
	keyDER, kerr := os.ReadFile(keyFile)
	certPEM, cerr := os.ReadFile(certFile)
	if kerr == nil && cerr == nil {
		key, err := x509.ParseECPrivateKey(keyDER)
		if err != nil {
			return nil, err
		}
		block, _ := pem.Decode(certPEM)
		cert, err := x509.ParseCertificate(block.Bytes)
		if err != nil {
			return nil, err
		}
		a := &Authority{RootKey: key, RootCert: cert}
		return a, a.newIntermediate()
	}
	a, err := New()
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	keyDER, err = x509.MarshalECPrivateKey(a.RootKey)
	if err != nil {
		return nil, err
	}
	if err := os.WriteFile(keyFile, keyDER, 0o600); err != nil {
		return nil, err
	}
	return a, os.WriteFile(certFile, a.RootPEM(), 0o644)
}

func must(b []byte, err error) []byte {
	if err != nil {
		panic(err)
	}
	return b
}

func sequence(parts ...[]byte) []byte {
	var body []byte
	for _, p := range parts {
		body = append(body, p...)
	}
	return must(asn1.Marshal(asn1.RawValue{Class: asn1.ClassUniversal, Tag: asn1.TagSequence, IsCompound: true, Bytes: body}))
}

func set(parts ...[]byte) []byte {
	var body []byte
	for _, p := range parts {
		body = append(body, p...)
	}
	return must(asn1.Marshal(asn1.RawValue{Class: asn1.ClassUniversal, Tag: asn1.TagSet, IsCompound: true, Bytes: body}))
}

func explicit(tag int, inner []byte) []byte {
	return must(asn1.Marshal(asn1.RawValue{Class: asn1.ClassContextSpecific, Tag: tag, IsCompound: true, Bytes: inner}))
}

// KeyDescription builds the DER attestation extension value.
func KeyDescription(o Options) []byte {
	appID := sequence(
		set(sequence(must(asn1.Marshal([]byte(o.Package))), must(asn1.Marshal(1)))),
		set(must(asn1.Marshal(o.SignatureDigest))),
	)
	sw := [][]byte{explicit(709, must(asn1.Marshal(appID)))}

	hw := [][]byte{
		explicit(1, set(must(asn1.Marshal(2)))), // purpose: SIGN
		explicit(2, must(asn1.Marshal(3))),      // algorithm: EC
		explicit(3, must(asn1.Marshal(256))),    // key size
	}
	if o.NoAuthRequired {
		hw = append(hw, explicit(503, must(asn1.Marshal(asn1.NullRawValue))))
	} else {
		hw = append(hw, explicit(504, must(asn1.Marshal(2)))) // userAuthType: biometric
	}
	if !o.OmitRootOfTrust {
		rot := sequence(
			must(asn1.Marshal(make([]byte, 32))),
			must(asn1.Marshal(o.DeviceLocked)),
			must(asn1.Marshal(asn1.Enumerated(o.BootState))),
			must(asn1.Marshal(make([]byte, 32))),
		)
		hw = append(hw, explicit(704, rot))
	}

	return sequence(
		must(asn1.Marshal(300)),
		must(asn1.Marshal(asn1.Enumerated(o.AttestationLevel))),
		must(asn1.Marshal(300)),
		must(asn1.Marshal(asn1.Enumerated(o.KeyLevel))),
		must(asn1.Marshal(o.Challenge)),
		must(asn1.Marshal([]byte{})),
		sequence(sw...),
		sequence(hw...),
	)
}

// Issue returns a chain for pub: leaf, batch certificate, root.
func (a *Authority) Issue(pub *ecdsa.PublicKey, o Options) ([][]byte, error) {
	leaf, err := a.leaf(pub, a.IntCert, a.IntKey, KeyDescription(o))
	if err != nil {
		return nil, err
	}
	return [][]byte{leaf, a.IntCert.Raw, a.RootCert.Raw}, nil
}

// SignLeaf issues a certificate for pub carrying the given attestation
// extension, signed by an arbitrary parent. Tests use it to forge a chain
// that has been extended below a genuine attested key.
func SignLeaf(pub *ecdsa.PublicKey, parent *x509.Certificate, parentKey *ecdsa.PrivateKey, ext []byte) ([]byte, error) {
	return (&Authority{}).leaf(pub, parent, parentKey, ext)
}

func (a *Authority) leaf(pub *ecdsa.PublicKey, parent *x509.Certificate, parentKey *ecdsa.PrivateKey, ext []byte) ([]byte, error) {
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "Android Keystore Key"},
		NotBefore:    time.Unix(0, 0),
		NotAfter:     time.Date(2048, 1, 1, 0, 0, 0, 0, time.UTC),
		KeyUsage:     x509.KeyUsageDigitalSignature,
	}
	if ext != nil {
		tmpl.ExtraExtensions = []pkix.Extension{{Id: oidKeyDescription, Value: ext}}
	}
	return x509.CreateCertificate(rand.Reader, tmpl, parent, pub, parentKey)
}

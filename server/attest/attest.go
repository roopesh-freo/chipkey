// Package attest verifies an Android Key Attestation certificate chain.
//
// It answers the questions a backend must ask before it stores a public key
// that claims to live in secure hardware:
//
//  1. Does the chain end at one of Google's attestation roots?
//  2. Is every certificate signed by the next, in date, and not revoked?
//  3. Is the attestation extension in the leaf, and only in the leaf?
//  4. What does that extension say: challenge, security level, verified boot
//     state, bootloader lock, and which app asked for the key?
//
// Reference: https://developer.android.com/privacy-and-security/security-key-attestation
package attest

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/x509"
	"encoding/asn1"
	"errors"
	"fmt"
	"math/big"
	"time"
)

// OIDKeyDescription is the X.509 extension that carries the attestation.
var OIDKeyDescription = asn1.ObjectIdentifier{1, 3, 6, 1, 4, 1, 11129, 2, 1, 17}

// SecurityLevel says where something happened: in plain Android, in the
// Trusted Execution Environment, or in a StrongBox secure chip.
type SecurityLevel int

const (
	Software SecurityLevel = iota
	TrustedEnvironment
	StrongBox
)

func (l SecurityLevel) String() string {
	switch l {
	case Software:
		return "software"
	case TrustedEnvironment:
		return "tee"
	case StrongBox:
		return "strongbox"
	}
	return fmt.Sprintf("unknown(%d)", int(l))
}

// BootState is the verified boot state reported by the bootloader.
type BootState int

const (
	Verified BootState = iota
	SelfSigned
	Unverified
	Failed
)

func (b BootState) String() string {
	switch b {
	case Verified:
		return "verified"
	case SelfSigned:
		return "self-signed"
	case Unverified:
		return "unverified"
	case Failed:
		return "failed"
	}
	return fmt.Sprintf("unknown(%d)", int(b))
}

// Authorization list tags we read. The full list is in the KeyMint HAL.
const (
	tagNoAuthRequired           = 503
	tagUserAuthType             = 504
	tagRootOfTrust              = 704
	tagAttestationApplicationID = 709
)

// Root is a trusted attestation root, pinned by public key. Google has
// re-issued its root certificate several times around the same key, so the
// SubjectPublicKeyInfo is the stable thing to pin, not the certificate.
type Root struct {
	Name string
	SPKI []byte
}

// Options control verification.
type Options struct {
	// Roots are the trusted roots. Use GoogleRoots() in production.
	Roots []Root
	// Revoked, if set, is asked about every certificate serial in the chain.
	Revoked func(serial *big.Int) (reason string, revoked bool)
	// Now overrides the clock, for tests.
	Now time.Time
}

// ErrUntrustedRoot means the chain is internally consistent but ends at a
// root we do not trust. Emulators and software-only keystores land here.
var ErrUntrustedRoot = errors.New("chain does not end at a trusted attestation root")

// Result is what a verified attestation tells the backend.
type Result struct {
	PublicKey *ecdsa.PublicKey
	// SPKI is the DER SubjectPublicKeyInfo of the attested key.
	SPKI []byte

	HasAttestation     bool
	Challenge          []byte
	AttestationVersion int
	// AttestationLevel is where the attestation itself was produced.
	AttestationLevel SecurityLevel
	// KeyLevel is where the private key lives. This is the tier.
	KeyLevel SecurityLevel

	HasRootOfTrust bool
	DeviceLocked   bool
	BootState      BootState

	// UserAuthRequired is true when the chip demands user authentication
	// before it will use the key.
	UserAuthRequired bool
	// UserAuthType is a bitmask: 1 = password or PIN, 2 = biometric.
	UserAuthType int

	PackageNames     []string
	SignatureDigests [][]byte

	RootName string
	ChainLen int
}

// Integrity reports whether the phone booted a verified OS with a locked
// bootloader. This comes from the bootloader via the chip, not from the app.
func (r *Result) Integrity() bool {
	return r.HasRootOfTrust && r.DeviceLocked && r.BootState == Verified
}

func parseChain(chainDER [][]byte) ([]*x509.Certificate, error) {
	if len(chainDER) == 0 {
		return nil, errors.New("empty certificate chain")
	}
	certs := make([]*x509.Certificate, 0, len(chainDER))
	for i, der := range chainDER {
		c, err := x509.ParseCertificate(der)
		if err != nil {
			return nil, fmt.Errorf("certificate %d does not parse: %w", i, err)
		}
		certs = append(certs, c)
	}
	return certs, nil
}

// Verify checks the chain, leaf first and root last, and returns what the
// attestation says. It does not compare the challenge or apply policy. The
// caller does that, because only the caller knows which nonce it issued.
func Verify(chainDER [][]byte, o Options) (*Result, error) {
	certs, err := parseChain(chainDER)
	if err != nil {
		return nil, err
	}
	if len(certs) < 2 {
		return nil, errors.New("chain has a single certificate, so nothing vouches for it")
	}
	now := o.Now
	if now.IsZero() {
		now = time.Now()
	}

	// 1. Every certificate is signed by the next one up.
	for i := 0; i < len(certs)-1; i++ {
		child, parent := certs[i], certs[i+1]
		if err := parent.CheckSignature(child.SignatureAlgorithm, child.RawTBSCertificate, child.Signature); err != nil {
			return nil, fmt.Errorf("certificate %d is not signed by certificate %d: %w", i, i+1, err)
		}
	}
	root := certs[len(certs)-1]
	if err := root.CheckSignature(root.SignatureAlgorithm, root.RawTBSCertificate, root.Signature); err != nil {
		return nil, fmt.Errorf("root certificate is not self-signed: %w", err)
	}

	// 2. The root's public key is one we pin.
	rootName := ""
	for _, tr := range o.Roots {
		if bytes.Equal(tr.SPKI, root.RawSubjectPublicKeyInfo) {
			rootName = tr.Name
			break
		}
	}
	if rootName == "" {
		return nil, fmt.Errorf("%w (root subject %q)", ErrUntrustedRoot, root.Subject.String())
	}

	// 3. Issuing certificates are in date. The leaf is skipped on purpose:
	// Keystore gives it the key's own validity, which is often 1970 to 2048.
	for i := 1; i < len(certs); i++ {
		if now.Before(certs[i].NotBefore) || now.After(certs[i].NotAfter) {
			return nil, fmt.Errorf("certificate %d is outside its validity period (%s to %s)",
				i, certs[i].NotBefore.Format(time.DateOnly), certs[i].NotAfter.Format(time.DateOnly))
		}
	}

	// 4. Nothing in the chain is on Google's revocation list.
	if o.Revoked != nil {
		for i, c := range certs {
			if reason, bad := o.Revoked(c.SerialNumber); bad {
				return nil, fmt.Errorf("certificate %d (serial %x) is revoked: %s", i, c.SerialNumber, reason)
			}
		}
	}

	res, err := describe(certs)
	if err != nil {
		return nil, err
	}
	if !res.HasAttestation {
		return nil, errors.New("leaf certificate has no attestation extension")
	}
	res.RootName = rootName
	return res, nil
}

// DescribeUnverified reads the leaf key and any attestation extension
// without trusting the chain. Use it only to enrol a device into the lowest
// tier, where nothing the device says is believed.
func DescribeUnverified(chainDER [][]byte) (*Result, error) {
	certs, err := parseChain(chainDER)
	if err != nil {
		return nil, err
	}
	return describe(certs)
}

func describe(certs []*x509.Certificate) (*Result, error) {
	leaf := certs[0]
	pub, ok := leaf.PublicKey.(*ecdsa.PublicKey)
	if !ok || pub.Curve != elliptic.P256() {
		return nil, errors.New("attested key is not an ECDSA P-256 key")
	}
	res := &Result{PublicKey: pub, SPKI: leaf.RawSubjectPublicKeyInfo, ChainLen: len(certs)}

	// The attestation that counts is the one closest to the root. A hardware
	// key can sign arbitrary bytes, so an attacker with one genuine attested
	// key could sign a forged "leaf" carrying a made-up extension and prepend
	// it. That forgery puts a second extension further from the root. If the
	// extension nearest the root is not in the leaf, the chain was extended.
	nearestRoot := -1
	for i := len(certs) - 1; i >= 0; i-- {
		if extension(certs[i]) != nil {
			nearestRoot = i
			break
		}
	}
	if nearestRoot == -1 {
		return res, nil
	}
	if nearestRoot != 0 {
		return nil, fmt.Errorf("attestation extension found in certificate %d, not the leaf: the chain was extended", nearestRoot)
	}
	if err := parseKeyDescription(extension(leaf), res); err != nil {
		return nil, fmt.Errorf("attestation extension: %w", err)
	}
	res.HasAttestation = true
	return res, nil
}

func extension(c *x509.Certificate) []byte {
	for _, e := range c.Extensions {
		if e.Id.Equal(OIDKeyDescription) {
			return e.Value
		}
	}
	return nil
}

// elements splits the content of a SEQUENCE or SET into its members.
func elements(content []byte) ([]asn1.RawValue, error) {
	var out []asn1.RawValue
	for len(content) > 0 {
		var rv asn1.RawValue
		rest, err := asn1.Unmarshal(content, &rv)
		if err != nil {
			return nil, err
		}
		out = append(out, rv)
		content = rest
	}
	return out, nil
}

func asInt(rv asn1.RawValue) (int, error) {
	if rv.Class != asn1.ClassUniversal || (rv.Tag != asn1.TagInteger && rv.Tag != asn1.TagEnum) {
		return 0, fmt.Errorf("expected INTEGER or ENUMERATED, got class %d tag %d", rv.Class, rv.Tag)
	}
	if len(rv.Bytes) == 0 || len(rv.Bytes) > 8 {
		return 0, errors.New("integer has an unusable length")
	}
	n := new(big.Int).SetBytes(rv.Bytes)
	if rv.Bytes[0]&0x80 != 0 {
		n.Sub(n, new(big.Int).Lsh(big.NewInt(1), uint(8*len(rv.Bytes))))
	}
	return int(n.Int64()), nil
}

func asOctets(rv asn1.RawValue) ([]byte, error) {
	if rv.Class != asn1.ClassUniversal || rv.Tag != asn1.TagOctetString {
		return nil, fmt.Errorf("expected OCTET STRING, got class %d tag %d", rv.Class, rv.Tag)
	}
	return rv.Bytes, nil
}

//	KeyDescription ::= SEQUENCE {
//	    attestationVersion        INTEGER,
//	    attestationSecurityLevel  SecurityLevel,
//	    keyMintVersion            INTEGER,
//	    keyMintSecurityLevel      SecurityLevel,
//	    attestationChallenge      OCTET_STRING,
//	    uniqueId                  OCTET_STRING,
//	    softwareEnforced          AuthorizationList,
//	    hardwareEnforced          AuthorizationList }
func parseKeyDescription(der []byte, res *Result) error {
	var seq asn1.RawValue
	if _, err := asn1.Unmarshal(der, &seq); err != nil {
		return err
	}
	if seq.Class != asn1.ClassUniversal || seq.Tag != asn1.TagSequence {
		return errors.New("KeyDescription is not a SEQUENCE")
	}
	f, err := elements(seq.Bytes)
	if err != nil {
		return err
	}
	if len(f) < 8 {
		return fmt.Errorf("KeyDescription has %d fields, want 8", len(f))
	}
	if res.AttestationVersion, err = asInt(f[0]); err != nil {
		return err
	}
	al, err := asInt(f[1])
	if err != nil {
		return err
	}
	kl, err := asInt(f[3])
	if err != nil {
		return err
	}
	res.AttestationLevel, res.KeyLevel = SecurityLevel(al), SecurityLevel(kl)
	if res.Challenge, err = asOctets(f[4]); err != nil {
		return err
	}

	sw, err := authList(f[6])
	if err != nil {
		return fmt.Errorf("softwareEnforced: %w", err)
	}
	hw, err := authList(f[7])
	if err != nil {
		return fmt.Errorf("hardwareEnforced: %w", err)
	}

	// The chip enforces user authentication unless NO_AUTH_REQUIRED appears.
	_, swNoAuth := sw[tagNoAuthRequired]
	_, hwNoAuth := hw[tagNoAuthRequired]
	res.UserAuthRequired = !swNoAuth && !hwNoAuth
	if inner, ok := hw[tagUserAuthType]; ok {
		var rv asn1.RawValue
		if _, err := asn1.Unmarshal(inner, &rv); err == nil {
			res.UserAuthType, _ = asInt(rv)
		}
	}

	// Root of trust is only believable from the hardware-enforced list.
	if inner, ok := hw[tagRootOfTrust]; ok {
		if err := parseRootOfTrust(inner, res); err != nil {
			return fmt.Errorf("rootOfTrust: %w", err)
		}
	}

	// The application ID is filled in by Android, so it sits in the
	// software-enforced list. It is as trustworthy as the OS, which is why
	// the verified boot state above matters.
	if inner, ok := sw[tagAttestationApplicationID]; ok {
		if err := parseApplicationID(inner, res); err != nil {
			return fmt.Errorf("attestationApplicationId: %w", err)
		}
	}
	return nil
}

// authList maps each context tag in an AuthorizationList to the DER inside
// its EXPLICIT wrapper.
func authList(seq asn1.RawValue) (map[int][]byte, error) {
	if seq.Class != asn1.ClassUniversal || seq.Tag != asn1.TagSequence {
		return nil, errors.New("AuthorizationList is not a SEQUENCE")
	}
	items, err := elements(seq.Bytes)
	if err != nil {
		return nil, err
	}
	m := make(map[int][]byte, len(items))
	for _, it := range items {
		if it.Class == asn1.ClassContextSpecific {
			m[it.Tag] = it.Bytes
		}
	}
	return m, nil
}

//	RootOfTrust ::= SEQUENCE {
//	    verifiedBootKey    OCTET_STRING,
//	    deviceLocked       BOOLEAN,
//	    verifiedBootState  VerifiedBootState,
//	    verifiedBootHash   OCTET_STRING }  -- attestation version 3 and later
func parseRootOfTrust(der []byte, res *Result) error {
	var seq asn1.RawValue
	if _, err := asn1.Unmarshal(der, &seq); err != nil {
		return err
	}
	f, err := elements(seq.Bytes)
	if err != nil {
		return err
	}
	if len(f) < 3 {
		return fmt.Errorf("has %d fields, want at least 3", len(f))
	}
	if f[1].Class != asn1.ClassUniversal || f[1].Tag != asn1.TagBoolean || len(f[1].Bytes) != 1 {
		return errors.New("deviceLocked is not a BOOLEAN")
	}
	state, err := asInt(f[2])
	if err != nil {
		return err
	}
	res.HasRootOfTrust = true
	res.DeviceLocked = f[1].Bytes[0] != 0
	res.BootState = BootState(state)
	return nil
}

//	AttestationApplicationId ::= SEQUENCE {
//	    package_infos      SET OF SEQUENCE { package_name OCTET_STRING, version INTEGER },
//	    signature_digests  SET OF OCTET_STRING }
//
// It arrives wrapped in an OCTET STRING.
func parseApplicationID(der []byte, res *Result) error {
	var wrapper asn1.RawValue
	if _, err := asn1.Unmarshal(der, &wrapper); err != nil {
		return err
	}
	inner, err := asOctets(wrapper)
	if err != nil {
		return err
	}
	var seq asn1.RawValue
	if _, err := asn1.Unmarshal(inner, &seq); err != nil {
		return err
	}
	f, err := elements(seq.Bytes)
	if err != nil {
		return err
	}
	if len(f) < 2 {
		return fmt.Errorf("has %d fields, want 2", len(f))
	}
	pkgs, err := elements(f[0].Bytes)
	if err != nil {
		return err
	}
	for _, p := range pkgs {
		pf, err := elements(p.Bytes)
		if err != nil {
			return err
		}
		if len(pf) < 1 {
			continue
		}
		name, err := asOctets(pf[0])
		if err != nil {
			return err
		}
		res.PackageNames = append(res.PackageNames, string(name))
	}
	digests, err := elements(f[1].Bytes)
	if err != nil {
		return err
	}
	for _, d := range digests {
		b, err := asOctets(d)
		if err != nil {
			return err
		}
		res.SignatureDigests = append(res.SignatureDigests, b)
	}
	return nil
}

package attest

import (
	"crypto/x509"
	"embed"
	"encoding/pem"
	"fmt"
	"os"
	"path"
)

// The PEM files in roots/ are Google's hardware attestation roots, copied
// from https://android.googleapis.com/attestation/root on 2026-11-12:
// the original RSA-4096 root and the ECDSA P-384 "Key Attestation CA1" root
// that newer, remotely provisioned devices chain to. Refresh them from that
// URL if Google adds another.
//
//go:embed roots/*.pem
var rootFiles embed.FS

var googleRootNames = map[string]string{
	"google_rsa_4096.pem":                 "Google hardware attestation root (RSA-4096)",
	"google_key_attestation_ca1_p384.pem": "Google Key Attestation CA1 (P-384)",
}

// GoogleRoots returns the pinned Google hardware attestation roots.
func GoogleRoots() ([]Root, error) {
	entries, err := rootFiles.ReadDir("roots")
	if err != nil {
		return nil, err
	}
	var roots []Root
	for _, e := range entries {
		data, err := rootFiles.ReadFile(path.Join("roots", e.Name()))
		if err != nil {
			return nil, err
		}
		r, err := rootFromPEM(googleRootNames[e.Name()], data)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", e.Name(), err)
		}
		roots = append(roots, r)
	}
	return roots, nil
}

// RootFromFile loads an extra root from a PEM file. It exists for rehearsal
// with the fake phone. Never point it at anything in production.
func RootFromFile(name, file string) (Root, error) {
	data, err := os.ReadFile(file)
	if err != nil {
		return Root{}, err
	}
	return rootFromPEM(name, data)
}

func rootFromPEM(name string, data []byte) (Root, error) {
	block, _ := pem.Decode(data)
	if block == nil || block.Type != "CERTIFICATE" {
		return Root{}, fmt.Errorf("no PEM certificate found")
	}
	c, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return Root{}, err
	}
	return Root{Name: name, SPKI: c.RawSubjectPublicKeyInfo}, nil
}

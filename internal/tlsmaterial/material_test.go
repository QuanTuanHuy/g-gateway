package tlsmaterial

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestNewCertificateParsesAndReturnsIndependentTLSValues(t *testing.T) {
	certificatePEM, privateKeyPEM := issueSelfSignedPair(t, "client.internal")

	material, err := NewCertificate("client", certificatePEM, privateKeyPEM)
	if err != nil {
		t.Fatal(err)
	}
	if material.ID() != "client" {
		t.Fatalf("ID() = %q, want client", material.ID())
	}
	if material.Fingerprint() == (Fingerprint{}) {
		t.Fatal("Fingerprint() is zero")
	}

	first := material.TLSCertificate()
	if first.Leaf == nil || first.Leaf.Subject.CommonName != "client.internal" {
		t.Fatalf("TLSCertificate().Leaf = %+v", first.Leaf)
	}
	first.Certificate[0][0] ^= 0xff
	first.OCSPStaple = []byte("changed")

	second := material.TLSCertificate()
	if second.Certificate[0][0] == first.Certificate[0][0] {
		t.Fatal("TLSCertificate() shares certificate bytes")
	}
	if len(second.OCSPStaple) != 0 {
		t.Fatalf("TLSCertificate().OCSPStaple = %q, want empty", second.OCSPStaple)
	}
}

func TestNewTrustBundleCanonicalizesOrderAndDuplicates(t *testing.T) {
	caA := issueRootCertificate(t, "Root A", 11)
	caB := issueRootCertificate(t, "Root B", 12)

	first, err := NewTrustBundle("roots", append(append([]byte{}, caA...), caB...))
	if err != nil {
		t.Fatal(err)
	}
	secondDocument := append(append(append([]byte{}, caB...), caA...), caA...)
	second, err := NewTrustBundle("roots", secondDocument)
	if err != nil {
		t.Fatal(err)
	}
	if first.Fingerprint() != second.Fingerprint() {
		t.Fatal("equivalent trust sets produced different fingerprints")
	}

	firstPool := first.CertPool()
	secondPool := first.CertPool()
	if firstPool == secondPool {
		t.Fatal("CertPool() reused a mutable pool")
	}
	if len(firstPool.Subjects()) != 2 || len(secondPool.Subjects()) != 2 {
		t.Fatalf("pool subjects = %d, %d; want 2", len(firstPool.Subjects()), len(secondPool.Subjects()))
	}
}

func TestNewTrustBundleRejectsNonCertificatePEM(t *testing.T) {
	document := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: []byte("not a certificate")})
	if _, err := NewTrustBundle("roots", document); err == nil {
		t.Fatal("NewTrustBundle() accepted a non-certificate PEM block")
	}
}

func TestNewCertificateRejectsMismatchedPrivateKey(t *testing.T) {
	certificatePEM, _ := issueSelfSignedPair(t, "client.internal")
	_, otherPrivateKeyPEM := issueSelfSignedPair(t, "other.internal")
	if _, err := NewCertificate("client", certificatePEM, otherPrivateKeyPEM); err == nil {
		t.Fatal("NewCertificate() accepted a mismatched private key")
	}
}

func TestMaterialConstructorsRejectInvalidDocuments(t *testing.T) {
	validCertificate, validKey := issueSelfSignedPair(t, "client.internal")
	tests := []struct {
		name string
		run  func() error
	}{
		{name: "empty certificate ID", run: func() error {
			_, err := NewCertificate("", validCertificate, validKey)
			return err
		}},
		{name: "malformed certificate", run: func() error {
			_, err := NewCertificate("client", []byte("invalid"), validKey)
			return err
		}},
		{name: "malformed private key", run: func() error {
			_, err := NewCertificate("client", validCertificate, []byte("invalid"))
			return err
		}},
		{name: "empty trust bundle", run: func() error {
			_, err := NewTrustBundle("roots", nil)
			return err
		}},
		{name: "malformed trust bundle DER", run: func() error {
			document := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: []byte("invalid")})
			_, err := NewTrustBundle("roots", document)
			return err
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if err := test.run(); err == nil {
				t.Fatal("constructor accepted invalid material")
			}
		})
	}
}

func TestLoadCertificateAndTrustBundleReportSourceBytes(t *testing.T) {
	certificatePEM, privateKeyPEM := issueSelfSignedPair(t, "client.internal")
	caPEM := issueRootCertificate(t, "Root", 21)
	directory := t.TempDir()
	certificatePath := writeTestFile(t, directory, "client.crt", certificatePEM)
	privateKeyPath := writeTestFile(t, directory, "client.key", privateKeyPEM)
	caPath := writeTestFile(t, directory, "ca.pem", caPEM)

	certificate, certificateBytes, err := LoadCertificate("client", certificatePath, privateKeyPath)
	if err != nil {
		t.Fatal(err)
	}
	if certificate.ID() != "client" ||
		certificateBytes != int64(len(certificatePEM)+len(privateKeyPEM)) {
		t.Fatalf("certificate = %q bytes=%d", certificate.ID(), certificateBytes)
	}
	bundle, bundleBytes, err := LoadTrustBundle("roots", caPath)
	if err != nil {
		t.Fatal(err)
	}
	if bundle.ID() != "roots" || bundleBytes != int64(len(caPEM)) {
		t.Fatalf("bundle = %q bytes=%d", bundle.ID(), bundleBytes)
	}
}

func TestLoadMaterialRejectsOversizedAndNonRegularFiles(t *testing.T) {
	directory := t.TempDir()
	oversized := writeTestFile(t, directory, "large.pem", bytes.Repeat([]byte{'x'}, int(MaxCAFileBytes)+1))
	if _, _, err := LoadTrustBundle("roots", oversized); err == nil ||
		!strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("LoadTrustBundle() error = %v, want size error", err)
	}
	if _, _, err := LoadTrustBundle("roots", directory); err == nil ||
		!strings.Contains(err.Error(), "regular file") {
		t.Fatalf("LoadTrustBundle() error = %v, want regular-file error", err)
	}
}

func issueSelfSignedPair(t *testing.T, commonName string) ([]byte, []byte) {
	t.Helper()
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	template := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: commonName},
		NotBefore:    now.Add(-time.Minute),
		NotAfter:     now.Add(time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
		DNSNames:     []string{commonName},
	}
	certificateDER, err := x509.CreateCertificate(rand.Reader, template, template, publicKey, privateKey)
	if err != nil {
		t.Fatal(err)
	}
	privateKeyDER, err := x509.MarshalPKCS8PrivateKey(privateKey)
	if err != nil {
		t.Fatal(err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: certificateDER}),
		pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: privateKeyDER})
}

func issueRootCertificate(t *testing.T, commonName string, serial int64) []byte {
	t.Helper()
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	template := &x509.Certificate{
		SerialNumber:          big.NewInt(serial),
		Subject:               pkix.Name{CommonName: commonName},
		NotBefore:             now.Add(-time.Minute),
		NotAfter:              now.Add(time.Hour),
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
		BasicConstraintsValid: true,
		IsCA:                  true,
	}
	certificateDER, err := x509.CreateCertificate(rand.Reader, template, template, publicKey, privateKey)
	if err != nil {
		t.Fatal(err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: certificateDER})
}

func writeTestFile(t *testing.T, directory, name string, data []byte) string {
	t.Helper()
	path := filepath.Join(directory, name)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

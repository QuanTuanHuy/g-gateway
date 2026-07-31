package tlsmaterial

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"testing"
	"time"
)

func FuzzCertificateDocument(f *testing.F) {
	certificatePEM, privateKeyPEM := fuzzCertificatePair(f, "certificate")
	f.Add(certificatePEM, privateKeyPEM)
	f.Add([]byte("not a certificate"), []byte("not a key"))
	f.Add([]byte{}, []byte{})

	f.Fuzz(func(t *testing.T, certificateDocument, privateKeyDocument []byte) {
		if len(certificateDocument)+len(privateKeyDocument) > 1<<20 {
			t.Skip()
		}
		first, firstErr := NewCertificate("fuzz", certificateDocument, privateKeyDocument)
		second, secondErr := NewCertificate("fuzz", certificateDocument, privateKeyDocument)
		if (firstErr == nil) != (secondErr == nil) {
			t.Fatalf("same certificate input produced inconsistent errors: %v, %v", firstErr, secondErr)
		}
		if firstErr == nil && first.Fingerprint() != second.Fingerprint() {
			t.Fatal("same certificate input produced different fingerprints")
		}
	})
}

func FuzzTrustBundleDocument(f *testing.F) {
	firstPEM, _ := fuzzCertificatePair(f, "first-root")
	secondPEM, _ := fuzzCertificatePair(f, "second-root")
	f.Add(firstPEM)
	f.Add(append(append([]byte(nil), firstPEM...), firstPEM...))
	f.Add(append(append([]byte(nil), firstPEM...), secondPEM...))
	f.Add(append(append([]byte(nil), secondPEM...), firstPEM...))
	f.Add([]byte("-----BEGIN CERTIFICATE-----\nmalformed\n"))
	f.Add([]byte{})

	f.Fuzz(func(t *testing.T, document []byte) {
		if len(document) > 1<<20 {
			t.Skip()
		}
		first, firstErr := NewTrustBundle("fuzz", document)
		second, secondErr := NewTrustBundle("fuzz", document)
		if (firstErr == nil) != (secondErr == nil) {
			t.Fatalf("same trust input produced inconsistent errors: %v, %v", firstErr, secondErr)
		}
		if firstErr == nil && first.Fingerprint() != second.Fingerprint() {
			t.Fatal("same trust input produced different fingerprints")
		}
	})
}

func fuzzCertificatePair(tb testing.TB, commonName string) ([]byte, []byte) {
	tb.Helper()
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		tb.Fatal(err)
	}
	now := time.Now()
	template := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: commonName},
		NotBefore:             now.Add(-time.Minute),
		NotAfter:              now.Add(time.Hour),
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature,
		BasicConstraintsValid: true,
		IsCA:                  true,
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, publicKey, privateKey)
	if err != nil {
		tb.Fatal(err)
	}
	privateDER, err := x509.MarshalPKCS8PrivateKey(privateKey)
	if err != nil {
		tb.Fatal(err)
	}
	certificatePEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	privateKeyPEM := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: privateDER})
	if len(bytes.TrimSpace(certificatePEM)) == 0 || len(bytes.TrimSpace(privateKeyPEM)) == 0 {
		tb.Fatal("generated empty fuzz seed")
	}
	return certificatePEM, privateKeyPEM
}

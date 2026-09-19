package downstreamtls

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestLoadStaticReturnsSNIIndependentCertificate(t *testing.T) {
	certificateFile, privateKeyFile := writeProviderCertificate(t)
	provider, err := LoadStatic(certificateFile, privateKeyFile)
	if err != nil {
		t.Fatal(err)
	}

	first, err := provider.GetCertificate(&tls.ClientHelloInfo{ServerName: "api.example"})
	if err != nil {
		t.Fatal(err)
	}
	second, err := provider.GetCertificate(&tls.ClientHelloInfo{ServerName: "other.example"})
	if err != nil {
		t.Fatal(err)
	}
	if len(first.Certificate) == 0 || len(second.Certificate) == 0 ||
		string(first.Certificate[0]) != string(second.Certificate[0]) {
		t.Fatal("GetCertificate returned different or empty chains by SNI")
	}
}

func TestLoadStaticOwnsCertificateData(t *testing.T) {
	certificateFile, privateKeyFile := writeProviderCertificate(t)
	provider, err := LoadStatic(certificateFile, privateKeyFile)
	if err != nil {
		t.Fatal(err)
	}
	before, err := provider.GetCertificate(nil)
	if err != nil {
		t.Fatal(err)
	}
	want := append([]byte(nil), before.Certificate[0]...)
	if err := os.WriteFile(certificateFile, []byte("mutated"), 0o600); err != nil {
		t.Fatal(err)
	}
	after, err := provider.GetCertificate(nil)
	if err != nil {
		t.Fatal(err)
	}
	if string(after.Certificate[0]) != string(want) {
		t.Fatal("provider retained mutable file-backed certificate data")
	}
}

func TestStaticProviderRejectsUnavailableCertificate(t *testing.T) {
	var provider *StaticProvider
	if _, err := provider.GetCertificate(nil); err == nil {
		t.Fatal("nil provider returned a certificate")
	}
	provider = new(StaticProvider)
	if _, err := provider.GetCertificate(nil); err == nil {
		t.Fatal("empty provider returned a certificate")
	}
}

func TestStaticProviderSelectReportsBoundedClass(t *testing.T) {
	certificateFile, privateKeyFile := writeProviderCertificate(t)
	provider, err := LoadStatic(certificateFile, privateKeyFile)
	if err != nil {
		t.Fatal(err)
	}
	certificate, selection, err := provider.Select(&tls.ClientHelloInfo{ServerName: "api.example"})
	if err != nil || certificate == nil || selection != SelectionDefault {
		t.Fatalf("Select() = (%v, %q, %v)", certificate, selection, err)
	}
}

func writeProviderCertificate(t *testing.T) (string, string) {
	t.Helper()
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	template := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "gateway.test"},
		NotBefore:    now.Add(-time.Minute),
		NotAfter:     now.Add(time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		DNSNames:     []string{"gateway.test"},
	}
	certificateDER, err := x509.CreateCertificate(rand.Reader, template, template, publicKey, privateKey)
	if err != nil {
		t.Fatal(err)
	}
	privateKeyDER, err := x509.MarshalPKCS8PrivateKey(privateKey)
	if err != nil {
		t.Fatal(err)
	}
	directory := t.TempDir()
	certificateFile := filepath.Join(directory, "server.crt")
	privateKeyFile := filepath.Join(directory, "server.key")
	if err := os.WriteFile(certificateFile, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: certificateDER}), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(privateKeyFile, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: privateKeyDER}), 0o600); err != nil {
		t.Fatal(err)
	}
	return certificateFile, privateKeyFile
}

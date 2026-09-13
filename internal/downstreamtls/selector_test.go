package downstreamtls

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"math/big"
	"strings"
	"testing"
	"time"

	"github.com/QuanTuanHuy/g-gateway/internal/model"
	"github.com/QuanTuanHuy/g-gateway/internal/tlsmaterial"
)

func TestSelectorUsesExactBeforeWildcardAndFallsBackToDefault(t *testing.T) {
	now := time.Date(2026, 9, 13, 12, 0, 0, 0, time.UTC)
	certificates := []*tlsmaterial.Certificate{
		newSelectorCertificate(t, "default", 1, []string{"default.example"}, "", now.Add(-time.Hour), now.Add(time.Hour)),
		newSelectorCertificate(t, "wild", 2, []string{"*.example.com"}, "", now.Add(-time.Hour), now.Add(2*time.Hour)),
		newSelectorCertificate(t, "exact", 3, []string{"api.example.com"}, "", now.Add(-time.Hour), now.Add(3*time.Hour)),
	}
	selector, err := Compile(&model.DownstreamTLSPolicy{
		DefaultCertificateRef: "default",
		SNIBindings: []model.SNIBinding{
			{CertificateRef: "wild", Hosts: []string{"*.example.com"}},
			{CertificateRef: "exact", Hosts: []string{"api.example.com"}},
		},
	}, certificates, now)
	if err != nil {
		t.Fatal(err)
	}

	assertSelection(t, selector, "API.EXAMPLE.COM.", SelectionExact, 3)
	assertSelection(t, selector, "shop.example.com", SelectionWildcard, 2)
	assertSelection(t, selector, "a.b.example.com", SelectionDefault, 1)
	assertSelection(t, selector, "127.0.0.1", SelectionDefault, 1)
	assertSelection(t, selector, "", SelectionDefault, 1)

	stats := selector.Stats()
	if stats.CertificateCount != 3 || stats.ExactCount != 1 || stats.WildcardCount != 1 ||
		!stats.EarliestExpiry.Equal(now.Add(time.Hour)) {
		t.Fatalf("Stats() = %+v", stats)
	}
}

func TestCompileRejectsInvalidPolicies(t *testing.T) {
	now := time.Date(2026, 9, 13, 12, 0, 0, 0, time.UTC)
	valid := func(id string, serial int64, names ...string) *tlsmaterial.Certificate {
		return newSelectorCertificate(t, id, serial, names, "", now.Add(-time.Hour), now.Add(time.Hour))
	}
	defaultCertificate := valid("default", 1, "default.example")
	exactCertificate := valid("exact", 2, "api.example.com")
	wildcardCertificate := valid("wild", 3, "*.example.com")

	tests := []struct {
		name         string
		policy       *model.DownstreamTLSPolicy
		certificates []*tlsmaterial.Certificate
		wantCode     string
	}{
		{name: "nil policy", wantCode: CodeDownstreamTLSRequired},
		{name: "empty default", policy: &model.DownstreamTLSPolicy{}, certificates: []*tlsmaterial.Certificate{defaultCertificate}, wantCode: CodeDefaultCertificateNotFound},
		{name: "missing default", policy: &model.DownstreamTLSPolicy{DefaultCertificateRef: "missing"}, certificates: []*tlsmaterial.Certificate{defaultCertificate}, wantCode: CodeDefaultCertificateNotFound},
		{name: "nil material", policy: &model.DownstreamTLSPolicy{DefaultCertificateRef: "default"}, certificates: []*tlsmaterial.Certificate{nil, defaultCertificate}, wantCode: CodeSNIBindingInvalid},
		{name: "duplicate material ID", policy: &model.DownstreamTLSPolicy{DefaultCertificateRef: "default"}, certificates: []*tlsmaterial.Certificate{defaultCertificate, defaultCertificate}, wantCode: CodeSNIBindingInvalid},
		{name: "empty binding ref", policy: &model.DownstreamTLSPolicy{DefaultCertificateRef: "default", SNIBindings: []model.SNIBinding{{Hosts: []string{"api.example.com"}}}}, certificates: []*tlsmaterial.Certificate{defaultCertificate}, wantCode: CodeSNIBindingInvalid},
		{name: "missing binding ref", policy: &model.DownstreamTLSPolicy{DefaultCertificateRef: "default", SNIBindings: []model.SNIBinding{{CertificateRef: "missing", Hosts: []string{"api.example.com"}}}}, certificates: []*tlsmaterial.Certificate{defaultCertificate}, wantCode: CodeSNIBindingInvalid},
		{name: "empty hosts", policy: &model.DownstreamTLSPolicy{DefaultCertificateRef: "default", SNIBindings: []model.SNIBinding{{CertificateRef: "exact"}}}, certificates: []*tlsmaterial.Certificate{defaultCertificate, exactCertificate}, wantCode: CodeSNIBindingInvalid},
		{name: "invalid wildcard", policy: &model.DownstreamTLSPolicy{DefaultCertificateRef: "default", SNIBindings: []model.SNIBinding{{CertificateRef: "wild", Hosts: []string{"*.com"}}}}, certificates: []*tlsmaterial.Certificate{defaultCertificate, wildcardCertificate}, wantCode: CodeSNIBindingInvalid},
		{name: "exact conflict", policy: &model.DownstreamTLSPolicy{DefaultCertificateRef: "default", SNIBindings: []model.SNIBinding{{CertificateRef: "exact", Hosts: []string{"API.example.com", "api.example.com."}}}}, certificates: []*tlsmaterial.Certificate{defaultCertificate, exactCertificate}, wantCode: CodeSNIBindingConflict},
		{name: "wildcard conflict", policy: &model.DownstreamTLSPolicy{DefaultCertificateRef: "default", SNIBindings: []model.SNIBinding{{CertificateRef: "wild", Hosts: []string{"*.Example.com", "*.example.com."}}}}, certificates: []*tlsmaterial.Certificate{defaultCertificate, wildcardCertificate}, wantCode: CodeSNIBindingConflict},
		{name: "exact SAN mismatch", policy: &model.DownstreamTLSPolicy{DefaultCertificateRef: "default", SNIBindings: []model.SNIBinding{{CertificateRef: "exact", Hosts: []string{"other.example.com"}}}}, certificates: []*tlsmaterial.Certificate{defaultCertificate, exactCertificate}, wantCode: CodeCertificateHostnameMismatch},
		{name: "wildcard SAN mismatch", policy: &model.DownstreamTLSPolicy{DefaultCertificateRef: "default", SNIBindings: []model.SNIBinding{{CertificateRef: "wild", Hosts: []string{"*.other.com"}}}}, certificates: []*tlsmaterial.Certificate{defaultCertificate, wildcardCertificate}, wantCode: CodeCertificateHostnameMismatch},
		{name: "common name only", policy: &model.DownstreamTLSPolicy{DefaultCertificateRef: "default", SNIBindings: []model.SNIBinding{{CertificateRef: "cn", Hosts: []string{"cn.example.com"}}}}, certificates: []*tlsmaterial.Certificate{defaultCertificate, newSelectorCertificate(t, "cn", 4, nil, "cn.example.com", now.Add(-time.Hour), now.Add(time.Hour))}, wantCode: CodeCertificateHostnameMismatch},
		{name: "not yet valid", policy: &model.DownstreamTLSPolicy{DefaultCertificateRef: "future"}, certificates: []*tlsmaterial.Certificate{newSelectorCertificate(t, "future", 5, []string{"future.example"}, "", now.Add(time.Second), now.Add(time.Hour))}, wantCode: CodeCertificateNotYetValid},
		{name: "expired", policy: &model.DownstreamTLSPolicy{DefaultCertificateRef: "expired"}, certificates: []*tlsmaterial.Certificate{newSelectorCertificate(t, "expired", 6, []string{"expired.example"}, "", now.Add(-time.Hour), now.Add(-time.Nanosecond))}, wantCode: CodeCertificateExpired},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := Compile(test.policy, test.certificates, now)
			var configErr *ConfigError
			if !errors.As(err, &configErr) || configErr.Code != test.wantCode {
				t.Fatalf("Compile() error = %v, want code %s", err, test.wantCode)
			}
		})
	}
}

func TestCompileAcceptsInclusiveValidityBounds(t *testing.T) {
	now := time.Date(2026, 9, 13, 12, 0, 0, 0, time.UTC)
	certificate := newSelectorCertificate(t, "default", 1, []string{"default.example"}, "", now, now)
	if _, err := Compile(&model.DownstreamTLSPolicy{DefaultCertificateRef: "default"}, []*tlsmaterial.Certificate{certificate}, now); err != nil {
		t.Fatalf("Compile() rejected inclusive validity bounds: %v", err)
	}
}

func TestCompileRejectsSNIIndexOverLimit(t *testing.T) {
	now := time.Date(2026, 9, 13, 12, 0, 0, 0, time.UTC)
	hosts := make([]string, MaxSNINames+1)
	for index := range hosts {
		hosts[index] = "api.example.com"
	}
	certificate := newSelectorCertificate(t, "default", 1, []string{"default.example", "api.example.com"}, "", now.Add(-time.Hour), now.Add(time.Hour))
	_, err := Compile(&model.DownstreamTLSPolicy{
		DefaultCertificateRef: "default",
		SNIBindings:           []model.SNIBinding{{CertificateRef: "default", Hosts: hosts}},
	}, []*tlsmaterial.Certificate{certificate}, now)
	var configErr *ConfigError
	if !errors.As(err, &configErr) || configErr.Code != CodeSNIIndexLimitExceeded {
		t.Fatalf("Compile() error = %v, want %s", err, CodeSNIIndexLimitExceeded)
	}
}

func TestSelectorRejectsUnavailableCertificate(t *testing.T) {
	var selector *Selector
	certificate, selection, err := selector.Select(&tls.ClientHelloInfo{ServerName: "api.example"})
	if certificate != nil || selection != SelectionError || !errors.Is(err, errCertificateUnavailable) {
		t.Fatalf("Select() = (%v, %q, %v)", certificate, selection, err)
	}
}

func assertSelection(t *testing.T, selector *Selector, serverName string, wantSelection Selection, wantSerial int64) {
	t.Helper()
	certificate, selection, err := selector.Select(&tls.ClientHelloInfo{ServerName: serverName})
	if err != nil {
		t.Fatal(err)
	}
	if selection != wantSelection || certificate.Leaf == nil || certificate.Leaf.SerialNumber.Int64() != wantSerial {
		t.Fatalf("Select(%q) = (serial %v, %q), want (%d, %q)", serverName, certificate.Leaf.SerialNumber, selection, wantSerial, wantSelection)
	}
}

func newSelectorCertificate(t testing.TB, id string, serial int64, dnsNames []string, commonName string, notBefore, notAfter time.Time) *tlsmaterial.Certificate {
	t.Helper()
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{
		SerialNumber: big.NewInt(serial),
		Subject:      pkix.Name{CommonName: commonName},
		NotBefore:    notBefore,
		NotAfter:     notAfter,
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		DNSNames:     append([]string(nil), dnsNames...),
	}
	certificateDER, err := x509.CreateCertificate(rand.Reader, template, template, publicKey, privateKey)
	if err != nil {
		t.Fatal(err)
	}
	privateKeyDER, err := x509.MarshalPKCS8PrivateKey(privateKey)
	if err != nil {
		t.Fatal(err)
	}
	certificate, err := tlsmaterial.NewCertificate(
		id,
		pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: certificateDER}),
		pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: privateKeyDER}),
	)
	if err != nil {
		t.Fatal(err)
	}
	return certificate
}

func TestConfigErrorDoesNotExposeCauseByDefault(t *testing.T) {
	err := &ConfigError{Code: CodeSNIBindingInvalid, ResourceID: "binding", Field: "hosts", Cause: errors.New(strings.Repeat("secret", 20))}
	if strings.Contains(err.Error(), "secret") {
		t.Fatalf("Error() exposed cause: %q", err)
	}
}

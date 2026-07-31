package upstream

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"strings"
	"testing"
	"time"

	"github.com/QuanTuanHuy/g-gateway/internal/model"
	"github.com/QuanTuanHuy/g-gateway/internal/tlsmaterial"
)

func TestNormalizeAcceptsApprovedSchemeProtocolMatrix(t *testing.T) {
	tests := []struct {
		name     string
		endpoint string
		protocol model.TransportProtocol
	}{
		{name: "http auto", endpoint: "http://upstream.example", protocol: model.TransportProtocolAuto},
		{name: "http one", endpoint: "http://upstream.example", protocol: model.TransportProtocolHTTP1},
		{name: "http two", endpoint: "http://upstream.example", protocol: model.TransportProtocolHTTP2},
		{name: "https auto", endpoint: "https://upstream.example", protocol: model.TransportProtocolAuto},
		{name: "https one", endpoint: "https://upstream.example", protocol: model.TransportProtocolHTTP1},
		{name: "https two", endpoint: "https://upstream.example", protocol: model.TransportProtocolHTTP2},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			resource := transportPolicyUpstream(test.endpoint, test.protocol, nil)
			got, err := Normalize([]model.Upstream{resource})
			if err != nil {
				t.Fatal(err)
			}
			if got[0].Transport.Protocol != test.protocol {
				t.Fatalf("protocol = %q, want %q", got[0].Transport.Protocol, test.protocol)
			}
		})
	}
}

func TestNormalizeDefaultsZeroProtocolToHTTP1(t *testing.T) {
	resource := transportPolicyUpstream("http://upstream.example", "", nil)
	got, err := Normalize([]model.Upstream{resource})
	if err != nil {
		t.Fatal(err)
	}
	if got[0].Transport.Protocol != model.TransportProtocolHTTP1 {
		t.Fatalf("protocol = %q, want http1", got[0].Transport.Protocol)
	}
}

func TestNormalizeRejectsInvalidSchemeProtocolAndTLSCombinations(t *testing.T) {
	tests := []struct {
		name      string
		resource  model.Upstream
		wantCode  string
		wantField string
	}{
		{
			name:      "unsupported scheme",
			resource:  transportPolicyUpstream("ftp://upstream.example", model.TransportProtocolHTTP1, nil),
			wantCode:  "UPSTREAM_ENDPOINT_INVALID",
			wantField: "upstreams[0].endpoints[0].url",
		},
		{
			name:      "unsupported protocol",
			resource:  transportPolicyUpstream("http://upstream.example", "http3", nil),
			wantCode:  "TRANSPORT_PROTOCOL_INVALID",
			wantField: "upstreams[0].transport.protocol",
		},
		{
			name: "mixed positive schemes",
			resource: func() model.Upstream {
				value := transportPolicyUpstream("http://upstream.example", model.TransportProtocolHTTP1, nil)
				value.Endpoints = append(value.Endpoints, model.Endpoint{URL: "https://secure.example", Weight: 1})
				return value
			}(),
			wantCode:  "UPSTREAM_SCHEME_MIXED",
			wantField: "upstreams[0].endpoints",
		},
		{
			name: "TLS policy on cleartext",
			resource: transportPolicyUpstream("http://upstream.example", model.TransportProtocolHTTP1, &model.UpstreamTLSPolicy{
				TrustBundleRef: "roots",
			}),
			wantCode:  "TRANSPORT_TLS_INVALID",
			wantField: "upstreams[0].transport.tls",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := Normalize([]model.Upstream{test.resource})
			assertConfigError(t, err, test.wantCode, test.wantField)
		})
	}
}

func TestNormalizeIgnoresWeightZeroEndpointScheme(t *testing.T) {
	resource := transportPolicyUpstream("https://secure.example", model.TransportProtocolHTTP2, nil)
	resource.Endpoints = append(resource.Endpoints, model.Endpoint{
		URL:    "http://disabled.example",
		Weight: 0,
	})
	if _, err := Normalize([]model.Upstream{resource}); err != nil {
		t.Fatal(err)
	}
}

func TestNormalizeCanonicalizesTLSServerName(t *testing.T) {
	resource := transportPolicyUpstream("https://127.0.0.1", model.TransportProtocolHTTP2, &model.UpstreamTLSPolicy{
		ServerName: "Orders.Internal.",
	})
	got, err := Normalize([]model.Upstream{resource})
	if err != nil {
		t.Fatal(err)
	}
	if got[0].Transport.TLS.ServerName != "orders.internal" {
		t.Fatalf("server name = %q", got[0].Transport.TLS.ServerName)
	}
}

func TestCompileTransportProfileResolvesOptionalMaterialReferences(t *testing.T) {
	resource := transportPolicyUpstream("https://orders.internal", model.TransportProtocolAuto, nil)
	normalized, err := Normalize([]model.Upstream{resource})
	if err != nil {
		t.Fatal(err)
	}
	materials, err := newMaterialIndex(model.ResourceSet{})
	if err != nil {
		t.Fatal(err)
	}
	profile, err := compileTransportProfile(normalized[0], materials)
	if err != nil {
		t.Fatal(err)
	}
	if profile.scheme != "https" ||
		profile.protocol != model.TransportProtocolAuto ||
		profile.trustBundle != nil ||
		profile.clientCertificate != nil {
		t.Fatalf("profile = %+v", profile)
	}
}

func TestCompileTransportProfileRejectsMissingMaterialReference(t *testing.T) {
	resource := transportPolicyUpstream("https://orders.internal", model.TransportProtocolHTTP2, &model.UpstreamTLSPolicy{
		TrustBundleRef:       "missing-roots",
		ClientCertificateRef: "missing-client",
	})
	normalized, err := Normalize([]model.Upstream{resource})
	if err != nil {
		t.Fatal(err)
	}
	materials, err := newMaterialIndex(model.ResourceSet{})
	if err != nil {
		t.Fatal(err)
	}
	_, err = compileTransportProfile(normalized[0], materials)
	assertConfigError(t, err, "TLS_MATERIAL_REF_NOT_FOUND", "upstreams.transport.tls.trust_bundle_ref")
	if err == nil || !strings.Contains(err.Error(), "missing-roots") {
		t.Fatalf("error = %v, want missing reference", err)
	}
}

func TestCompileTransportProfileResolvesConfiguredMaterial(t *testing.T) {
	certificatePEM, privateKeyPEM := profileTestPair(t)
	certificate, err := tlsmaterial.NewCertificate("client", certificatePEM, privateKeyPEM)
	if err != nil {
		t.Fatal(err)
	}
	bundle, err := tlsmaterial.NewTrustBundle("roots", certificatePEM)
	if err != nil {
		t.Fatal(err)
	}
	materials, err := newMaterialIndex(model.ResourceSet{
		Certificates: []*tlsmaterial.Certificate{certificate},
		TrustBundles: []*tlsmaterial.TrustBundle{bundle},
	})
	if err != nil {
		t.Fatal(err)
	}
	resource := transportPolicyUpstream("https://orders.internal", model.TransportProtocolHTTP2, &model.UpstreamTLSPolicy{
		TrustBundleRef:       "roots",
		ClientCertificateRef: "client",
		ServerName:           "orders.internal",
	})
	normalized, err := Normalize([]model.Upstream{resource})
	if err != nil {
		t.Fatal(err)
	}
	profile, err := compileTransportProfile(normalized[0], materials)
	if err != nil {
		t.Fatal(err)
	}
	if profile.trustBundle != bundle ||
		profile.clientCertificate != certificate ||
		profile.serverName != "orders.internal" {
		t.Fatalf("profile material = %+v", profile)
	}
}

func TestNewMaterialIndexRejectsCrossKindDuplicateID(t *testing.T) {
	certificatePEM, privateKeyPEM := profileTestPair(t)
	certificate, err := tlsmaterial.NewCertificate("shared", certificatePEM, privateKeyPEM)
	if err != nil {
		t.Fatal(err)
	}
	bundle, err := tlsmaterial.NewTrustBundle("shared", certificatePEM)
	if err != nil {
		t.Fatal(err)
	}
	_, err = newMaterialIndex(model.ResourceSet{
		Certificates: []*tlsmaterial.Certificate{certificate},
		TrustBundles: []*tlsmaterial.TrustBundle{bundle},
	})
	assertConfigError(t, err, "TLS_MATERIAL_ID_DUPLICATE", "trust_bundles[0].id")
}

func transportPolicyUpstream(
	endpoint string,
	protocol model.TransportProtocol,
	tlsPolicy *model.UpstreamTLSPolicy,
) model.Upstream {
	return model.Upstream{
		ID:        "orders",
		Endpoints: []model.Endpoint{{URL: endpoint, Weight: 1}},
		Balancer:  model.BalancerPolicy{Type: model.BalancerWeightedRoundRobin},
		Transport: model.TransportConfig{
			Protocol:                  protocol,
			TLS:                       tlsPolicy,
			DialTimeout:               validTransportConfig().DialTimeout,
			ResponseHeaderTimeout:     validTransportConfig().ResponseHeaderTimeout,
			IdleConnectionTimeout:     validTransportConfig().IdleConnectionTimeout,
			MaxIdleConnections:        validTransportConfig().MaxIdleConnections,
			MaxIdleConnectionsPerHost: validTransportConfig().MaxIdleConnectionsPerHost,
		},
	}
}

func profileTestPair(t *testing.T) ([]byte, []byte) {
	t.Helper()
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	template := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "Phase 3C1 Test Root"},
		NotBefore:             now.Add(-time.Minute),
		NotAfter:              now.Add(time.Hour),
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
		BasicConstraintsValid: true,
		IsCA:                  true,
		DNSNames:              []string{"orders.internal"},
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

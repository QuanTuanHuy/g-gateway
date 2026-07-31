package upstream

import (
	"testing"

	"github.com/QuanTuanHuy/g-gateway/internal/model"
)

func FuzzNormalizeEndpoint(f *testing.F) {
	for _, seed := range []string{
		"http://example.com",
		"http://127.0.0.1:8080/",
		"http://[::1]:80",
		"http://user@example.com",
		"https://example.com",
		"https://127.0.0.1:8443/",
		"https://[::1]:443",
	} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, raw string) {
		resources := []model.Upstream{validUpstreamWith(model.Endpoint{
			URL:    raw,
			Weight: 1,
		})}
		_, _ = Normalize(resources)
	})
}

func FuzzTransportPolicy(f *testing.F) {
	for _, seed := range []struct {
		scheme   string
		protocol string
		withTLS  bool
	}{
		{scheme: "http", protocol: "auto"},
		{scheme: "http", protocol: "http1"},
		{scheme: "http", protocol: "http2"},
		{scheme: "https", protocol: "auto"},
		{scheme: "https", protocol: "http1"},
		{scheme: "https", protocol: "http2", withTLS: true},
	} {
		f.Add(seed.scheme, seed.protocol, seed.withTLS)
	}
	f.Fuzz(func(t *testing.T, scheme, protocol string, withTLS bool) {
		var tlsPolicy *model.UpstreamTLSPolicy
		if withTLS {
			tlsPolicy = &model.UpstreamTLSPolicy{ServerName: "upstream.example"}
		}
		resource := transportPolicyUpstream(
			scheme+"://upstream.example",
			model.TransportProtocol(protocol),
			tlsPolicy,
		)
		normalized, err := Normalize([]model.Upstream{resource})
		if err != nil {
			return
		}
		got := normalized[0].Transport.Protocol
		if got != model.TransportProtocolAuto &&
			got != model.TransportProtocolHTTP1 &&
			got != model.TransportProtocolHTTP2 {
			t.Fatalf("successful protocol = %q", got)
		}
	})
}

package upstream

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

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

func FuzzTransportKey(f *testing.F) {
	for _, seed := range []struct {
		scheme   string
		protocol string
		server   string
		timeout  int64
	}{
		{scheme: "http", protocol: "http1"},
		{scheme: "http", protocol: "http2"},
		{scheme: "https", protocol: "auto", server: "orders.internal"},
		{scheme: "https", protocol: "http1", server: "orders.internal"},
		{scheme: "https", protocol: "http2", server: "orders.internal"},
	} {
		f.Add(seed.scheme, seed.protocol, seed.server, seed.timeout)
	}
	f.Fuzz(func(t *testing.T, scheme, protocol, serverName string, timeoutNanos int64) {
		timeout := time.Duration(timeoutNanos)
		profile := transportProfile{
			scheme:     scheme,
			protocol:   model.TransportProtocol(protocol),
			serverName: serverName,
			transport: model.TransportConfig{
				Protocol:              model.TransportProtocol(protocol),
				DialTimeout:           timeout,
				ResponseHeaderTimeout: timeout,
				IdleConnectionTimeout: timeout,
			},
		}
		first := makeTransportKey(profile)
		second := makeTransportKey(profile)
		if first != second {
			t.Fatalf("same profile produced different keys: %+v, %+v", first, second)
		}
	})
}

func FuzzTLSFailureRedaction(f *testing.F) {
	for _, seed := range []string{
		"private-key-material",
		"orders.internal",
		"-----BEGIN CERTIFICATE-----",
	} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, secret string) {
		if secret == "" {
			return
		}
		for _, class := range []TLSFailureClass{
			TLSFailureTrust,
			TLSFailureHostname,
			TLSFailureClientIdentity,
			TLSFailureProtocol,
			TLSFailureHandshake,
		} {
			failure := &TLSFailureError{
				Class: class,
				Err:   fmt.Errorf("sensitive peer detail: %s", secret),
			}
			wrapped := fmt.Errorf("transport attempt: %w", failure)
			if strings.Contains(failure.Error(), secret) {
				t.Fatalf("public failure leaked fuzz input for class %q", class)
			}
			got, ok := TLSFailureClassOf(wrapped)
			if !ok || got != class {
				t.Fatalf("typed wrapper class=(%q,%t), want (%q,true)", got, ok, class)
			}
			if !errors.Is(wrapped, failure.Err) {
				t.Fatal("typed wrapper did not retain internal cause")
			}
		}
	})
}

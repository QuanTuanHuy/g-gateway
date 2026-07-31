package upstream

import (
	"crypto/tls"
	"net/http"
	"testing"
	"time"

	"github.com/QuanTuanHuy/g-gateway/internal/model"
	"github.com/QuanTuanHuy/g-gateway/internal/tlsmaterial"
)

func TestTransportKeyIncludesEveryConnectionSemantic(t *testing.T) {
	base := transportTestProfile(t, "https", model.TransportProtocolHTTP2)
	key := makeTransportKey(base)

	tests := []struct {
		name   string
		change func(*transportProfile)
	}{
		{name: "scheme", change: func(profile *transportProfile) { profile.scheme = "http" }},
		{name: "protocol", change: func(profile *transportProfile) { profile.protocol = model.TransportProtocolHTTP1 }},
		{name: "dial timeout", change: func(profile *transportProfile) { profile.transport.DialTimeout += time.Nanosecond }},
		{name: "response header timeout", change: func(profile *transportProfile) { profile.transport.ResponseHeaderTimeout += time.Nanosecond }},
		{name: "idle connection timeout", change: func(profile *transportProfile) { profile.transport.IdleConnectionTimeout += time.Nanosecond }},
		{name: "max idle connections", change: func(profile *transportProfile) { profile.transport.MaxIdleConnections++ }},
		{name: "max idle per host", change: func(profile *transportProfile) { profile.transport.MaxIdleConnectionsPerHost++ }},
		{name: "system roots", change: func(profile *transportProfile) { profile.trustBundle = nil }},
		{name: "trust fingerprint", change: func(profile *transportProfile) {
			profile.trustBundle = transportTestProfile(t, "https", model.TransportProtocolHTTP2).trustBundle
		}},
		{name: "no client identity", change: func(profile *transportProfile) { profile.clientCertificate = nil }},
		{name: "client fingerprint", change: func(profile *transportProfile) {
			profile.clientCertificate = transportTestProfile(t, "https", model.TransportProtocolHTTP2).clientCertificate
		}},
		{name: "server name", change: func(profile *transportProfile) { profile.serverName = "changed.internal" }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			changed := base
			test.change(&changed)
			if key == makeTransportKey(changed) {
				t.Fatalf("%s missing from transport key", test.name)
			}
		})
	}
	if key != makeTransportKey(base) {
		t.Fatal("identical complete profiles produced different keys")
	}
	if !key.disableCompression ||
		!key.tlsEnabled ||
		key.tlsPolicyVersion != 1 ||
		key.minTLSVersion != tls.VersionTLS12 {
		t.Fatalf("fixed transport semantics = %+v", key)
	}
}

func TestTransportRuntimeConfiguresNativeProtocolMatrix(t *testing.T) {
	tests := []struct {
		name             string
		scheme           string
		protocol         model.TransportProtocol
		http1            bool
		http2            bool
		unencryptedHTTP2 bool
		nextProtocols    []string
	}{
		{name: "http auto", scheme: "http", protocol: model.TransportProtocolAuto, http1: true},
		{name: "http one", scheme: "http", protocol: model.TransportProtocolHTTP1, http1: true},
		{name: "http two", scheme: "http", protocol: model.TransportProtocolHTTP2, unencryptedHTTP2: true},
		{name: "https auto", scheme: "https", protocol: model.TransportProtocolAuto, http1: true, http2: true, nextProtocols: []string{"h2", "http/1.1"}},
		{name: "https one", scheme: "https", protocol: model.TransportProtocolHTTP1, http1: true, nextProtocols: []string{"http/1.1"}},
		{name: "https two", scheme: "https", protocol: model.TransportProtocolHTTP2, http2: true, nextProtocols: []string{"h2"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			runtime := newTransportRuntime(transportTestProfile(t, test.scheme, test.protocol))
			t.Cleanup(runtime.CloseIdleConnections)
			for name, transport := range map[string]*http.Transport{
				"production": runtime.production,
				"probe":      runtime.probe,
			} {
				if transport == nil || transport.Protocols == nil {
					t.Fatalf("%s transport or protocols are nil", name)
				}
				if transport.Protocols.HTTP1() != test.http1 ||
					transport.Protocols.HTTP2() != test.http2 ||
					transport.Protocols.UnencryptedHTTP2() != test.unencryptedHTTP2 {
					t.Fatalf("%s protocols = %s", name, transport.Protocols)
				}
				if transport.DisableCompression != true {
					t.Fatalf("%s compression enabled", name)
				}
				if test.scheme == "http" {
					if transport.TLSClientConfig != nil {
						t.Fatalf("%s cleartext TLS config = %+v", name, transport.TLSClientConfig)
					}
					continue
				}
				if transport.TLSClientConfig == nil ||
					transport.TLSClientConfig.MinVersion != tls.VersionTLS12 {
					t.Fatalf("%s TLS config = %+v", name, transport.TLSClientConfig)
				}
				if !equalStrings(transport.TLSClientConfig.NextProtos, test.nextProtocols) {
					t.Fatalf("%s NextProtos = %v, want %v", name, transport.TLSClientConfig.NextProtos, test.nextProtocols)
				}
			}
			if runtime.production == runtime.probe {
				t.Fatal("production and probe transports share a pool")
			}
			if test.scheme == "https" &&
				runtime.production.TLSClientConfig.ClientSessionCache ==
					runtime.probe.TLSClientConfig.ClientSessionCache {
				t.Fatal("production and probe transports share a TLS session cache")
			}
		})
	}
}

func TestTransportRuntimeCloseIdleConnectionsClosesBothPoolsOnce(t *testing.T) {
	runtime := newTransportRuntime(transportTestProfile(t, "http", model.TransportProtocolHTTP1))
	productionCalls := 0
	probeCalls := 0
	runtime.closeProductionIdle = func() { productionCalls++ }
	runtime.closeProbeIdle = func() { probeCalls++ }

	runtime.CloseIdleConnections()
	runtime.CloseIdleConnections()
	if productionCalls != 1 || probeCalls != 1 {
		t.Fatalf("close calls production=%d probe=%d, want 1 each", productionCalls, probeCalls)
	}
}

func transportTestProfile(
	t *testing.T,
	scheme string,
	protocol model.TransportProtocol,
) transportProfile {
	t.Helper()
	config := validTransportConfig()
	config.Protocol = protocol
	profile := transportProfile{
		scheme:    scheme,
		protocol:  protocol,
		transport: config,
	}
	if scheme == "http" {
		return profile
	}
	certificatePEM, privateKeyPEM := profileTestPair(t)
	certificate, err := tlsmaterial.NewCertificate("client", certificatePEM, privateKeyPEM)
	if err != nil {
		t.Fatal(err)
	}
	bundle, err := tlsmaterial.NewTrustBundle("roots", certificatePEM)
	if err != nil {
		t.Fatal(err)
	}
	profile.transport.TLS = &model.UpstreamTLSPolicy{
		TrustBundleRef:       "roots",
		ClientCertificateRef: "client",
		ServerName:           "orders.internal",
	}
	profile.trustBundle = bundle
	profile.clientCertificate = certificate
	profile.serverName = "orders.internal"
	return profile
}

func equalStrings(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

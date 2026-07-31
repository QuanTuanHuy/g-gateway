package upstream

import (
	"context"
	"crypto/tls"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
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
			runtime := newTransportRuntime(transportTestProfile(t, test.scheme, test.protocol), nil)
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
	runtime := newTransportRuntime(transportTestProfile(t, "http", model.TransportProtocolHTTP1), nil)
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

func TestTLSDialSeparatesVerificationNameFromHTTPHost(t *testing.T) {
	certificatePEM, privateKeyPEM := profileTestPair(t)
	serverCertificate, err := tls.X509KeyPair(certificatePEM, privateKeyPEM)
	if err != nil {
		t.Fatal(err)
	}
	sni := make(chan string, 1)
	host := make(chan string, 1)
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(
		writer http.ResponseWriter,
		request *http.Request,
	) {
		host <- request.Host
		_, _ = io.WriteString(writer, "ok")
	}))
	server.TLS = &tls.Config{
		Certificates: []tls.Certificate{serverCertificate},
		NextProtos:   []string{"http/1.1"},
		GetConfigForClient: func(hello *tls.ClientHelloInfo) (*tls.Config, error) {
			sni <- hello.ServerName
			return nil, nil
		},
	}
	server.StartTLS()
	defer server.Close()

	profile := tlsServerProfile(t, certificatePEM, model.TransportProtocolHTTP1)
	profile.serverName = "orders.internal"
	observer := &recordingTLSObserver{}
	runtime := newTransportRuntime(profile, observer)
	defer runtime.CloseIdleConnections()

	request, err := http.NewRequestWithContext(context.Background(), http.MethodGet, server.URL, nil)
	if err != nil {
		t.Fatal(err)
	}
	request.Host = "api.example.test"
	response, err := runtime.RoundTrip(request)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.Copy(io.Discard, response.Body)
	_ = response.Body.Close()

	if gotSNI, gotHost := <-sni, <-host; gotSNI != "orders.internal" || gotHost != "api.example.test" {
		t.Fatalf("SNI=%q Host=%q", gotSNI, gotHost)
	}
	if len(observer.handshakes) != 1 ||
		observer.handshakes[0] != (tlsHandshakeObservation{
			result: "success", mode: "server_auth", protocol: model.TransportProtocolHTTP1,
		}) {
		t.Fatalf("handshakes=%+v", observer.handshakes)
	}
	if len(observer.failures) != 0 {
		t.Fatalf("failures=%v", observer.failures)
	}
}

func TestTLSDialDerivesSNIFromEndpointHost(t *testing.T) {
	certificatePEM, privateKeyPEM := profileTestPair(t)
	serverCertificate, err := tls.X509KeyPair(certificatePEM, privateKeyPEM)
	if err != nil {
		t.Fatal(err)
	}
	sni := make(chan string, 1)
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.WriteHeader(http.StatusNoContent)
	}))
	server.TLS = &tls.Config{
		Certificates: []tls.Certificate{serverCertificate},
		NextProtos:   []string{"http/1.1"},
		GetConfigForClient: func(hello *tls.ClientHelloInfo) (*tls.Config, error) {
			sni <- hello.ServerName
			return nil, nil
		},
	}
	server.StartTLS()
	defer server.Close()

	profile := tlsServerProfile(t, certificatePEM, model.TransportProtocolHTTP1)
	runtime := newTransportRuntime(profile, nil)
	defer runtime.CloseIdleConnections()
	endpoint := strings.Replace(server.URL, "127.0.0.1", "localhost", 1)
	response, err := runtime.RoundTrip(mustRequest(t, context.Background(), endpoint))
	if err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()
	if got := <-sni; got != "localhost" {
		t.Fatalf("SNI=%q, want localhost", got)
	}
}

func TestStrictALPNReturnsTypedProtocolFailure(t *testing.T) {
	certificatePEM, privateKeyPEM := profileTestPair(t)
	serverCertificate, err := tls.X509KeyPair(certificatePEM, privateKeyPEM)
	if err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	tlsListener := tls.NewListener(listener, &tls.Config{
		Certificates: []tls.Certificate{serverCertificate},
	})
	defer tlsListener.Close()
	serverDone := make(chan error, 1)
	go func() {
		connection, acceptErr := tlsListener.Accept()
		if acceptErr != nil {
			serverDone <- acceptErr
			return
		}
		defer connection.Close()
		serverDone <- connection.(*tls.Conn).Handshake()
	}()

	profile := tlsServerProfile(t, certificatePEM, model.TransportProtocolHTTP2)
	profile.serverName = "orders.internal"
	observer := &recordingTLSObserver{}
	runtime := newTransportRuntime(profile, observer)
	defer runtime.CloseIdleConnections()

	endpoint := "https://" + listener.Addr().String()
	_, err = runtime.RoundTrip(mustRequest(t, context.Background(), endpoint))
	class, ok := TLSFailureClassOf(err)
	if !ok || class != TLSFailureProtocol {
		t.Fatalf("RoundTrip error=%T %v, class=(%q,%v)", err, err, class, ok)
	}
	if len(observer.handshakes) != 1 ||
		observer.handshakes[0].result != "failure" ||
		len(observer.failures) != 1 ||
		observer.failures[0] != TLSFailureProtocol {
		t.Fatalf("handshakes=%+v failures=%v", observer.handshakes, observer.failures)
	}
	if err := <-serverDone; err != nil {
		t.Fatalf("server handshake error=%v", err)
	}
}

func tlsServerProfile(
	t *testing.T,
	certificatePEM []byte,
	protocol model.TransportProtocol,
) transportProfile {
	t.Helper()
	bundle, err := tlsmaterial.NewTrustBundle("server-roots", certificatePEM)
	if err != nil {
		t.Fatal(err)
	}
	config := validTransportConfig()
	config.Protocol = protocol
	return transportProfile{
		scheme:      "https",
		protocol:    protocol,
		transport:   config,
		trustBundle: bundle,
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

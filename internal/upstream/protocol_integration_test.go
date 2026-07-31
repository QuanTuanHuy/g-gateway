package upstream

import (
	"bufio"
	"context"
	"crypto/x509"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"github.com/QuanTuanHuy/g-gateway/internal/model"
	"github.com/QuanTuanHuy/g-gateway/internal/tlsmaterial"
)

func TestUpstreamProtocolMatrix(t *testing.T) {
	pki := newUpstreamTestPKI(t, "protocol-root")
	serverCertificatePEM, serverKeyPEM := pki.issue(t, certificateRequest{
		commonName: "orders.internal",
		dnsNames:   []string{"orders.internal"},
		usages:     []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	})
	handler := http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("X-Protocol-Major", strconv.Itoa(request.ProtoMajor))
		writer.WriteHeader(http.StatusNoContent)
	})
	http1 := httptest.NewServer(handler)
	defer http1.Close()
	h2c := startH2CServer(t, handler)
	httpsH1 := startUpstreamTLSServer(t, upstreamTLSServerOptions{
		certificatePEM: serverCertificatePEM,
		privateKeyPEM:  serverKeyPEM,
	}, handler)
	httpsDual := startUpstreamTLSServer(t, upstreamTLSServerOptions{
		certificatePEM: serverCertificatePEM,
		privateKeyPEM:  serverKeyPEM,
		nextProtocols:  []string{"h2", "http/1.1"},
		http2:          true,
	}, handler)
	trust := mustTrustBundle(t, "protocol-root", pki.rootPEM)

	tests := []struct {
		name       string
		rawURL     string
		protocol   model.TransportProtocol
		trust      bool
		serverName string
		wantMajor  int
	}{
		{name: "http auto", rawURL: http1.URL, protocol: model.TransportProtocolAuto, wantMajor: 1},
		{name: "http one", rawURL: http1.URL, protocol: model.TransportProtocolHTTP1, wantMajor: 1},
		{name: "http two", rawURL: h2c, protocol: model.TransportProtocolHTTP2, wantMajor: 2},
		{name: "https auto prefers two", rawURL: httpsDual, protocol: model.TransportProtocolAuto, trust: true, serverName: "orders.internal", wantMajor: 2},
		{name: "https auto falls back one", rawURL: httpsH1, protocol: model.TransportProtocolAuto, trust: true, serverName: "orders.internal", wantMajor: 1},
		{name: "https one", rawURL: httpsDual, protocol: model.TransportProtocolHTTP1, trust: true, serverName: "orders.internal", wantMajor: 1},
		{name: "https two", rawURL: httpsDual, protocol: model.TransportProtocolHTTP2, trust: true, serverName: "orders.internal", wantMajor: 2},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var profileTrust *tlsmaterial.TrustBundle
			if test.trust {
				profileTrust = trust
			}
			runtime := newTransportRuntime(
				integrationTransportProfile(
					test.rawURL,
					test.protocol,
					profileTrust,
					nil,
					test.serverName,
				),
				nil,
			)
			defer runtime.CloseIdleConnections()
			response, err := runtime.RoundTrip(mustRequest(t, context.Background(), test.rawURL))
			if err != nil {
				t.Fatal(err)
			}
			_ = response.Body.Close()
			if got := response.Header.Get("X-Protocol-Major"); got != strconv.Itoa(test.wantMajor) {
				t.Fatalf("upstream protocol major=%q, want %d", got, test.wantMajor)
			}
		})
	}
}

func TestStrictHTTPSHTTP2RejectsMissingALPN(t *testing.T) {
	pki := newUpstreamTestPKI(t, "protocol-root")
	serverCertificatePEM, serverKeyPEM := pki.issue(t, certificateRequest{
		commonName: "orders.internal",
		dnsNames:   []string{"orders.internal"},
		usages:     []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	})
	server := startUpstreamTLSServer(t, upstreamTLSServerOptions{
		certificatePEM: serverCertificatePEM,
		privateKeyPEM:  serverKeyPEM,
	}, http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.WriteHeader(http.StatusNoContent)
	}))
	runtime := newTransportRuntime(
		integrationTransportProfile(
			server,
			model.TransportProtocolHTTP2,
			mustTrustBundle(t, "protocol-root", pki.rootPEM),
			nil,
			"orders.internal",
		),
		nil,
	)
	defer runtime.CloseIdleConnections()

	_, err := runtime.RoundTrip(mustRequest(t, context.Background(), server))
	class, ok := TLSFailureClassOf(err)
	if !ok || class != TLSFailureProtocol {
		t.Fatalf("RoundTrip error=%T %v, class=(%q,%v)", err, err, class, ok)
	}
}

func TestHTTP2AndH2CStreamChunksTrailersAndCancellation(t *testing.T) {
	pki := newUpstreamTestPKI(t, "stream-root")
	serverCertificatePEM, serverKeyPEM := pki.issue(t, certificateRequest{
		commonName: "orders.internal",
		dnsNames:   []string{"orders.internal"},
		usages:     []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	})
	trust := mustTrustBundle(t, "stream-root", pki.rootPEM)
	tests := []struct {
		name    string
		start   func(*testing.T, http.Handler) string
		profile func(string) transportProfile
	}{
		{
			name: "https HTTP2",
			start: func(t *testing.T, handler http.Handler) string {
				return startUpstreamTLSServer(t, upstreamTLSServerOptions{
					certificatePEM: serverCertificatePEM,
					privateKeyPEM:  serverKeyPEM,
					nextProtocols:  []string{"h2"},
					http2:          true,
				}, handler)
			},
			profile: func(rawURL string) transportProfile {
				return integrationTransportProfile(
					rawURL,
					model.TransportProtocolHTTP2,
					trust,
					nil,
					"orders.internal",
				)
			},
		},
		{
			name:  "h2c",
			start: startH2CServer,
			profile: func(rawURL string) transportProfile {
				return integrationTransportProfile(
					rawURL,
					model.TransportProtocolHTTP2,
					nil,
					nil,
					"",
				)
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name+" trailers", func(t *testing.T) {
			release := make(chan struct{})
			server := test.start(t, http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
				writer.Header().Set("Trailer", "X-Final")
				_, _ = io.WriteString(writer, "first\n")
				writer.(http.Flusher).Flush()
				<-release
				_, _ = io.WriteString(writer, "second\n")
				writer.Header().Set("X-Final", "done")
			}))
			runtime := newTransportRuntime(test.profile(server), nil)
			defer runtime.CloseIdleConnections()
			response, err := runtime.RoundTrip(mustRequest(t, context.Background(), server))
			if err != nil {
				t.Fatal(err)
			}
			reader := bufio.NewReader(response.Body)
			first, err := reader.ReadString('\n')
			if err != nil || first != "first\n" {
				t.Fatalf("first chunk=%q err=%v", first, err)
			}
			close(release)
			remaining, err := io.ReadAll(reader)
			if err != nil {
				t.Fatal(err)
			}
			_ = response.Body.Close()
			if string(remaining) != "second\n" || response.Trailer.Get("X-Final") != "done" {
				t.Fatalf("remaining=%q trailer=%q", remaining, response.Trailer.Get("X-Final"))
			}
		})

		t.Run(test.name+" cancellation", func(t *testing.T) {
			canceled := make(chan struct{})
			server := test.start(t, http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
				_, _ = io.WriteString(writer, "started\n")
				writer.(http.Flusher).Flush()
				<-request.Context().Done()
				close(canceled)
			}))
			runtime := newTransportRuntime(test.profile(server), nil)
			defer runtime.CloseIdleConnections()
			ctx, cancel := context.WithCancel(context.Background())
			response, err := runtime.RoundTrip(mustRequest(t, ctx, server))
			if err != nil {
				t.Fatal(err)
			}
			reader := bufio.NewReader(response.Body)
			if _, err := reader.ReadString('\n'); err != nil {
				t.Fatal(err)
			}
			cancel()
			_ = response.Body.Close()
			select {
			case <-canceled:
			case <-time.After(time.Second):
				t.Fatal("upstream did not observe stream cancellation")
			}
		})
	}
}

func startH2CServer(t *testing.T, handler http.Handler) string {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	protocols := new(http.Protocols)
	protocols.SetUnencryptedHTTP2(true)
	server := &http.Server{Handler: handler, Protocols: protocols}
	go func() {
		_ = server.Serve(listener)
	}()
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = server.Shutdown(ctx)
	})
	return "http://" + listener.Addr().String()
}

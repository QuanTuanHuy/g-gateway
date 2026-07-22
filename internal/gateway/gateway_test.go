package gateway

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"io"
	"log/slog"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/QuanTuanHuy/g-gateway/internal/config"
	"github.com/QuanTuanHuy/g-gateway/internal/model"
)

func TestStartBindsAdminBeforeTrafficAndBecomesReady(t *testing.T) {
	fixture := newGatewayFixture(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "ok")
	}))
	addresses, err := fixture.gateway.Start()
	if err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	defer fixture.shutdown(t)

	for name, address := range map[string]string{"http": addresses.HTTP, "https": addresses.HTTPS, "admin": addresses.Admin} {
		if address == "" {
			t.Fatalf("%s address is empty", name)
		}
	}
	assertHTTPStatus(t, "http://"+loopbackAddress(t, addresses.Admin)+"/readyz", http.StatusOK, nil)
}

func TestStartFailureNeverBecomesReady(t *testing.T) {
	occupied, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer occupied.Close()

	fixture := newGatewayFixture(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	fixture.gateway.httpServer.Addr = occupied.Addr().String()

	_, err = fixture.gateway.Start()
	if err == nil {
		t.Fatal("Start() error = nil, want bind failure")
	}
	readiness := httptest.NewRecorder()
	fixture.gateway.telemetry.AdminHandler().ServeHTTP(readiness, httptest.NewRequest(http.MethodGet, "http://admin/readyz", nil))
	if readiness.Code != http.StatusServiceUnavailable {
		t.Fatalf("readiness status = %d after bind failure, want 503", readiness.Code)
	}
}

func TestNewRejectsMismatchedKeyPairBeforeBind(t *testing.T) {
	certFile, _ := writeCertificatePair(t)
	_, otherKey := writeCertificatePair(t)
	bootstrap := testBootstrap(certFile, otherKey)
	resources := testResources("http://127.0.0.1:8080")

	_, err := New(bootstrap, resources, testLogger())
	if err == nil || !strings.Contains(err.Error(), "load TLS key pair") {
		t.Fatalf("New() error = %v, want TLS key-pair error", err)
	}
}

func TestServesHTTP1Cleartext(t *testing.T) {
	fixture := newGatewayFixture(t, http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		_, _ = io.WriteString(w, request.Proto)
	}))
	addresses, err := fixture.gateway.Start()
	if err != nil {
		t.Fatal(err)
	}
	defer fixture.shutdown(t)

	response, err := http.Get("http://" + loopbackAddress(t, addresses.HTTP) + "/hello")
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	body, _ := io.ReadAll(response.Body)
	if response.ProtoMajor != 1 || string(body) != "HTTP/1.1" {
		t.Fatalf("downstream=%s upstream=%q", response.Proto, body)
	}
}

func TestServesHTTP1AndHTTP2OverTLS(t *testing.T) {
	fixture := newGatewayFixture(t, http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		_, _ = io.WriteString(w, request.Proto)
	}))
	addresses, err := fixture.gateway.Start()
	if err != nil {
		t.Fatal(err)
	}
	defer fixture.shutdown(t)
	target := "https://" + loopbackAddress(t, addresses.HTTPS) + "/hello"

	h1Transport := &http.Transport{TLSClientConfig: insecureTLSConfig(), ForceAttemptHTTP2: false}
	h1Protocols := new(http.Protocols)
	h1Protocols.SetHTTP1(true)
	h1Transport.Protocols = h1Protocols
	assertProtocol(t, &http.Client{Transport: h1Transport}, target, 1, "HTTP/1.1")

	h2Transport := &http.Transport{TLSClientConfig: insecureTLSConfig(), ForceAttemptHTTP2: true}
	h2Protocols := new(http.Protocols)
	h2Protocols.SetHTTP1(true)
	h2Protocols.SetHTTP2(true)
	h2Transport.Protocols = h2Protocols
	assertProtocol(t, &http.Client{Transport: h2Transport}, target, 2, "HTTP/1.1")
}

func TestShutdownFlipsReadinessBeforeDrain(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	fixture := newGatewayFixture(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		close(started)
		<-release
		_, _ = io.WriteString(w, "done")
	}))
	addresses, err := fixture.gateway.Start()
	if err != nil {
		t.Fatal(err)
	}

	trafficURL := "http://" + loopbackAddress(t, addresses.HTTP) + "/hello"
	requestDone := make(chan error, 1)
	go func() {
		response, err := http.Get(trafficURL)
		if err == nil {
			_, _ = io.Copy(io.Discard, response.Body)
			err = response.Body.Close()
		}
		requestDone <- err
	}()
	waitSignal(t, started, "upstream request start")

	shutdownDone := make(chan error, 1)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		shutdownDone <- fixture.gateway.Shutdown(ctx)
	}()
	assertEventuallyStatus(t, "http://"+loopbackAddress(t, addresses.Admin)+"/readyz", http.StatusServiceUnavailable)
	close(release)

	if err := <-requestDone; err != nil {
		t.Fatalf("in-flight request error = %v", err)
	}
	if err := <-shutdownDone; err != nil {
		t.Fatalf("Shutdown() error = %v", err)
	}
}

func TestShutdownDrainsInFlightRequest(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	fixture := newGatewayFixture(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		close(started)
		<-release
		_, _ = io.WriteString(w, "drained")
	}))
	addresses, err := fixture.gateway.Start()
	if err != nil {
		t.Fatal(err)
	}

	trafficURL := "http://" + loopbackAddress(t, addresses.HTTP) + "/hello"
	responseBody := make(chan string, 1)
	go func() {
		response, err := http.Get(trafficURL)
		if err != nil {
			responseBody <- "error: " + err.Error()
			return
		}
		defer response.Body.Close()
		body, _ := io.ReadAll(response.Body)
		responseBody <- string(body)
	}()
	waitSignal(t, started, "upstream request start")

	shutdownDone := make(chan error, 1)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		shutdownDone <- fixture.gateway.Shutdown(ctx)
	}()
	close(release)
	if got := <-responseBody; got != "drained" {
		t.Fatalf("response body = %q", got)
	}
	if err := <-shutdownDone; err != nil {
		t.Fatalf("Shutdown() error = %v", err)
	}
}

func TestShutdownDeadlineCancelsRemainingRequests(t *testing.T) {
	started := make(chan struct{})
	canceled := make(chan struct{})
	fixture := newGatewayFixture(t, http.HandlerFunc(func(_ http.ResponseWriter, request *http.Request) {
		close(started)
		<-request.Context().Done()
		close(canceled)
	}))
	addresses, err := fixture.gateway.Start()
	if err != nil {
		t.Fatal(err)
	}
	trafficURL := "http://" + loopbackAddress(t, addresses.HTTP) + "/hello"
	go func() {
		_, _ = http.Get(trafficURL)
	}()
	waitSignal(t, started, "upstream request start")

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	if err := fixture.gateway.Shutdown(ctx); err == nil {
		t.Fatal("Shutdown() error = nil, want deadline error")
	}
	waitSignal(t, canceled, "upstream request cancellation")
}

func TestPanicBeforeCommitReturnsStable500(t *testing.T) {
	handler := recoverPanics(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		panic("secret panic detail")
	}), testLogger())
	recorder := httptest.NewRecorder()

	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "http://gateway/hello", nil))

	if recorder.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d; body=%q", recorder.Code, recorder.Body.String())
	}
	var body map[string]string
	if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body["code"] != "INTERNAL_ERROR" || body["message"] != "internal server error" || strings.Contains(recorder.Body.String(), "secret") {
		t.Fatalf("body = %#v", body)
	}
}

type gatewayFixture struct {
	gateway  *Gateway
	upstream *httptest.Server
}

func newGatewayFixture(t *testing.T, upstreamHandler http.Handler) *gatewayFixture {
	t.Helper()
	upstreamServer := httptest.NewServer(upstreamHandler)
	certFile, keyFile := writeCertificatePair(t)
	gateway, err := New(testBootstrap(certFile, keyFile), testResources(upstreamServer.URL), testLogger())
	if err != nil {
		upstreamServer.Close()
		t.Fatalf("New() error = %v", err)
	}
	t.Cleanup(upstreamServer.Close)
	return &gatewayFixture{gateway: gateway, upstream: upstreamServer}
}

func (f *gatewayFixture) shutdown(t *testing.T) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := f.gateway.Shutdown(ctx); err != nil {
		t.Fatalf("Shutdown() error = %v", err)
	}
}

func testBootstrap(certFile, keyFile string) config.BootstrapConfig {
	return config.BootstrapConfig{
		HTTP:  config.ListenerConfig{Address: "127.0.0.1:0"},
		HTTPS: config.TLSListenerConfig{Address: "127.0.0.1:0", CertificateFile: certFile, PrivateKeyFile: keyFile},
		Admin: config.ListenerConfig{Address: "127.0.0.1:0"},
		Server: config.ServerConfig{
			ReadHeaderTimeout:   time.Second,
			IdleTimeout:         time.Minute,
			ShutdownTimeout:     time.Second,
			MaxHeaderBytes:      1 << 20,
			MaxRequestBodyBytes: 1 << 20,
		},
	}
}

func testResources(endpoint string) model.ResourceSet {
	return model.ResourceSet{
		Routes: []model.Route{{
			ID: "baseline",
			Match: model.RouteMatch{
				Path:    "/hello",
				Methods: []string{http.MethodGet, http.MethodPost},
			},
			UpstreamRef: "baseline",
		}},
		Upstreams: []model.Upstream{{
			ID:        "baseline",
			Endpoints: []string{endpoint},
			Transport: model.TransportConfig{
				DialTimeout:               time.Second,
				ResponseHeaderTimeout:     time.Second,
				IdleConnectionTimeout:     time.Minute,
				MaxIdleConnections:        32,
				MaxIdleConnectionsPerHost: 32,
			},
		}},
	}
}

func writeCertificatePair(t *testing.T) (string, string) {
	t.Helper()
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "localhost"},
		NotBefore:    time.Now().Add(-time.Minute),
		NotAfter:     time.Now().Add(time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		IPAddresses:  []net.IP{net.ParseIP("127.0.0.1")},
	}
	certificateDER, err := x509.CreateCertificate(rand.Reader, template, template, publicKey, privateKey)
	if err != nil {
		t.Fatal(err)
	}
	privateDER, err := x509.MarshalPKCS8PrivateKey(privateKey)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	certFile := filepath.Join(dir, "server.crt")
	keyFile := filepath.Join(dir, "server.key")
	if err := os.WriteFile(certFile, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: certificateDER}), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(keyFile, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: privateDER}), 0o600); err != nil {
		t.Fatal(err)
	}
	return certFile, keyFile
}

func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func insecureTLSConfig() *tls.Config {
	return &tls.Config{InsecureSkipVerify: true} // Test certificate is generated per test.
}

func loopbackAddress(t *testing.T, address string) string {
	t.Helper()
	_, port, err := net.SplitHostPort(address)
	if err != nil {
		t.Fatal(err)
	}
	return net.JoinHostPort("127.0.0.1", port)
}

func assertHTTPStatus(t *testing.T, target string, want int, client *http.Client) {
	t.Helper()
	if client == nil {
		client = http.DefaultClient
	}
	response, err := client.Get(target)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != want {
		body, _ := io.ReadAll(response.Body)
		t.Fatalf("GET %s status = %d, want %d; body=%q", target, response.StatusCode, want, body)
	}
}

func assertEventuallyStatus(t *testing.T, target string, want int) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		response, err := http.Get(target)
		if err == nil {
			_ = response.Body.Close()
			if response.StatusCode == want {
				return
			}
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("GET %s did not reach status %d", target, want)
}

func assertProtocol(t *testing.T, client *http.Client, target string, downstreamMajor int, upstreamProto string) {
	t.Helper()
	response, err := client.Get(target)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	body, _ := io.ReadAll(response.Body)
	if response.ProtoMajor != downstreamMajor || string(body) != upstreamProto {
		t.Fatalf("downstream=%s upstream=%q", response.Proto, body)
	}
}

func waitSignal(t *testing.T, signal <-chan struct{}, name string) {
	t.Helper()
	select {
	case <-signal:
	case <-time.After(time.Second):
		t.Fatalf("timed out waiting for %s", name)
	}
}

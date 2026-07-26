package integration_test

import (
	"bufio"
	"context"
	"crypto/ed25519"
	"crypto/rand"
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
	"github.com/QuanTuanHuy/g-gateway/internal/gateway"
	"github.com/QuanTuanHuy/g-gateway/internal/model"
	"github.com/QuanTuanHuy/g-gateway/internal/testupstream"
)

func TestHTTP1HeadersAndConnectionReuse(t *testing.T) {
	upstream := httptest.NewServer(testupstream.New(discardLogger()))
	defer upstream.Close()
	addresses := startGateway(t, upstream.URL, "/headers", 1<<20, time.Second)
	client := &http.Client{Transport: &http.Transport{MaxIdleConnsPerHost: 2}}
	defer client.CloseIdleConnections()

	for range 2 {
		request, _ := http.NewRequest(http.MethodGet, "http://"+loopback(t, addresses.HTTP)+"/headers?raw=%2F", nil)
		request.Host = "original.example:8443"
		request.Header.Set("X-Forwarded-For", "198.51.100.99")
		request.Header.Set("Connection", "X-Secret")
		request.Header.Set("X-Secret", "remove-me")
		response, err := client.Do(request)
		if err != nil {
			t.Fatal(err)
		}
		var body headersBody
		if err := json.NewDecoder(response.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		_ = response.Body.Close()
		if response.ProtoMajor != 1 || body.Protocol != "HTTP/1.1" {
			t.Fatalf("downstream=%s upstream=%s", response.Proto, body.Protocol)
		}
		if body.Host != "original.example:8443" || body.Headers.Get("X-Forwarded-For") == "198.51.100.99" {
			t.Fatalf("forwarded headers=%+v", body)
		}
		if body.Headers.Get("X-Secret") != "" || body.Headers.Get("X-Forwarded-Port") != "8443" {
			t.Fatalf("hop/forwarding headers=%v", body.Headers)
		}
	}

	state := readUpstreamState(t, upstream.URL)
	if state.Requests != 2 || state.Connections != 1 {
		t.Fatalf("upstream state=%+v, want two requests over one connection", state)
	}
}

func TestStreamsRequestAndResponse(t *testing.T) {
	upstream := httptest.NewServer(testupstream.New(discardLogger()))
	defer upstream.Close()
	echoAddresses := startGateway(t, upstream.URL, "/echo", 1<<20, time.Second)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	reader, writer := io.Pipe()
	defer writer.CloseWithError(context.Canceled)
	request, _ := http.NewRequestWithContext(ctx, http.MethodPost, "http://"+loopback(t, echoAddresses.HTTP)+"/echo", reader)
	responseReady := make(chan *http.Response, 1)
	requestError := make(chan error, 1)
	go func() {
		response, err := http.DefaultClient.Do(request)
		if err != nil {
			requestError <- err
			return
		}
		responseReady <- response
	}()
	if _, err := writer.Write([]byte("first")); err != nil {
		t.Fatal(err)
	}
	var response *http.Response
	select {
	case response = <-responseReady:
	case err := <-requestError:
		t.Fatal(err)
	case <-time.After(time.Second):
		_ = writer.CloseWithError(context.DeadlineExceeded)
		cancel()
		t.Fatal("response headers were buffered until request completion")
	}
	first := make([]byte, 5)
	if _, err := io.ReadFull(response.Body, first); err != nil || string(first) != "first" {
		t.Fatalf("first echoed chunk=%q err=%v", first, err)
	}
	if _, err := writer.Write([]byte("second")); err != nil {
		t.Fatal(err)
	}
	_ = writer.Close()
	rest, _ := io.ReadAll(response.Body)
	_ = response.Body.Close()
	if string(rest) != "second" {
		t.Fatalf("remaining echo=%q", rest)
	}

	streamAddresses := startGateway(t, upstream.URL, "/stream", 1<<20, time.Second)
	streamResponse, err := http.Get("http://" + loopback(t, streamAddresses.HTTP) + "/stream")
	if err != nil {
		t.Fatal(err)
	}
	scanner := bufio.NewReader(streamResponse.Body)
	line, err := scanner.ReadString('\n')
	if err != nil || line != "first\n" {
		t.Fatalf("first stream chunk=%q err=%v", line, err)
	}
	remaining, _ := io.ReadAll(streamResponse.Body)
	_ = streamResponse.Body.Close()
	if string(remaining) != "second\n" {
		t.Fatalf("remaining stream=%q", remaining)
	}
}

func TestCancellationAndTrailers(t *testing.T) {
	upstream := httptest.NewServer(testupstream.New(discardLogger()))
	defer upstream.Close()
	delayAddresses := startGateway(t, upstream.URL, "/delay/10s", 1<<20, 15*time.Second)
	ctx, cancel := context.WithCancel(context.Background())
	request, _ := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+loopback(t, delayAddresses.HTTP)+"/delay/10s", nil)
	done := make(chan error, 1)
	go func() {
		_, err := http.DefaultClient.Do(request)
		done <- err
	}()
	waitUpstreamState(t, upstream.URL, func(state upstreamState) bool { return state.Requests == 1 })
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("downstream cancellation did not return")
	}
	waitUpstreamState(t, upstream.URL, func(state upstreamState) bool { return state.Cancellations == 1 })

	resetUpstreamState(t, upstream.URL)
	trailerAddresses := startGateway(t, upstream.URL, "/trailers", 1<<20, time.Second)
	response, err := http.Get("http://" + loopback(t, trailerAddresses.HTTP) + "/trailers")
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.Copy(io.Discard, response.Body)
	_ = response.Body.Close()
	if response.Trailer.Get("X-Checksum") != "abc123" {
		t.Fatalf("trailers=%v", response.Trailer)
	}
}

func TestStableFailuresAndRequestGuards(t *testing.T) {
	closed, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	closedAddress := closed.Addr().String()
	_ = closed.Close()
	connectAddresses := startGateway(t, "http://"+closedAddress, "/hello", 4, time.Second)
	assertGatewayError(t, http.MethodGet, "http://"+loopback(t, connectAddresses.HTTP)+"/hello", nil, http.StatusBadGateway, "UPSTREAM_CONNECTION_FAILED", nil)
	assertGatewayError(t, http.MethodPost, "http://"+loopback(t, connectAddresses.HTTP)+"/hello", strings.NewReader("12345"), http.StatusRequestEntityTooLarge, "REQUEST_BODY_TOO_LARGE", nil)
	upgradeHeaders := make(http.Header)
	upgradeHeaders.Set("Connection", "Upgrade")
	upgradeHeaders.Set("Upgrade", "websocket")
	assertGatewayError(t, http.MethodGet, "http://"+loopback(t, connectAddresses.HTTP)+"/hello", nil, http.StatusNotImplemented, "UPGRADE_NOT_SUPPORTED", upgradeHeaders)

	upstream := httptest.NewServer(testupstream.New(discardLogger()))
	defer upstream.Close()
	timeoutAddresses := startGateway(t, upstream.URL, "/delay/1s", 1024, 20*time.Millisecond)
	assertGatewayError(t, http.MethodGet, "http://"+loopback(t, timeoutAddresses.HTTP)+"/delay/1s", nil, http.StatusGatewayTimeout, "UPSTREAM_TIMEOUT", nil)
	closeAddresses := startGateway(t, upstream.URL, "/close", 1024, time.Second)
	assertGatewayError(t, http.MethodGet, "http://"+loopback(t, closeAddresses.HTTP)+"/close", nil, http.StatusBadGateway, "UPSTREAM_CONNECTION_FAILED", nil)
}

type headersBody struct {
	Host     string      `json:"host"`
	Protocol string      `json:"protocol"`
	Headers  http.Header `json:"headers"`
}

type upstreamState struct {
	Requests      int64 `json:"requests"`
	Connections   int   `json:"connections"`
	Cancellations int64 `json:"cancellations"`
}

func startGateway(t *testing.T, endpoint, routePath string, maxBody int64, responseTimeout time.Duration) gateway.Addresses {
	t.Helper()
	certFile, keyFile := writeCertificatePair(t)
	bootstrap := config.BootstrapConfig{
		HTTP:  config.ListenerConfig{Address: "127.0.0.1:0"},
		HTTPS: config.TLSListenerConfig{Address: "127.0.0.1:0", CertificateFile: certFile, PrivateKeyFile: keyFile},
		Admin: config.ListenerConfig{Address: "127.0.0.1:0"},
		Server: config.ServerConfig{
			ReadHeaderTimeout:   time.Second,
			IdleTimeout:         time.Minute,
			ShutdownTimeout:     3 * time.Second,
			MaxHeaderBytes:      1 << 20,
			MaxRequestBodyBytes: maxBody,
		},
	}
	resources := model.ResourceSet{
		Routes: []model.Route{{ID: "integration", Match: model.RouteMatch{Path: routePath, Methods: []string{http.MethodGet, http.MethodPost}}, UpstreamRef: "integration"}},
		Upstreams: []model.Upstream{{
			ID:        "integration",
			Endpoints: []model.Endpoint{{URL: endpoint, Weight: 1}},
			Balancer:  model.BalancerPolicy{Type: model.BalancerWeightedRoundRobin},
			Transport: model.TransportConfig{
				DialTimeout:               time.Second,
				ResponseHeaderTimeout:     responseTimeout,
				IdleConnectionTimeout:     time.Minute,
				MaxIdleConnections:        32,
				MaxIdleConnectionsPerHost: 32,
			},
		}},
	}
	instance, err := gateway.New(bootstrap, resources, discardLogger())
	if err != nil {
		t.Fatal(err)
	}
	addresses, err := instance.Start()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		if err := instance.Shutdown(ctx); err != nil {
			t.Errorf("Shutdown() error=%v", err)
		}
	})
	return addresses
}

func assertGatewayError(t *testing.T, method, target string, body io.Reader, status int, code string, headers http.Header) {
	t.Helper()
	request, _ := http.NewRequest(method, target, body)
	request.Header = headers
	if request.Header == nil {
		request.Header = make(http.Header)
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	var errorBody map[string]string
	if err := json.NewDecoder(response.Body).Decode(&errorBody); err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != status || errorBody["code"] != code {
		t.Fatalf("response=%d body=%v, want %d %s", response.StatusCode, errorBody, status, code)
	}
}

func readUpstreamState(t *testing.T, baseURL string) upstreamState {
	t.Helper()
	response, err := http.Get(baseURL + "/debug/state")
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	var state upstreamState
	if err := json.NewDecoder(response.Body).Decode(&state); err != nil {
		t.Fatal(err)
	}
	return state
}

func waitUpstreamState(t *testing.T, baseURL string, condition func(upstreamState) bool) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if condition(readUpstreamState(t, baseURL)) {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("upstream state condition not reached")
}

func resetUpstreamState(t *testing.T, baseURL string) {
	t.Helper()
	request, _ := http.NewRequest(http.MethodPost, baseURL+"/debug/reset", nil)
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()
}

func loopback(t *testing.T, address string) string {
	t.Helper()
	_, port, err := net.SplitHostPort(address)
	if err != nil {
		t.Fatal(err)
	}
	return net.JoinHostPort("127.0.0.1", port)
}

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
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

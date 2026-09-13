package proxy

import (
	"bufio"
	"context"
	"crypto/sha1" // #nosec G505 -- test fixture implements RFC 6455.
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/QuanTuanHuy/g-gateway/internal/model"
	"github.com/QuanTuanHuy/g-gateway/internal/plugin"
	"github.com/QuanTuanHuy/g-gateway/internal/requestctx"
	gatewayruntime "github.com/QuanTuanHuy/g-gateway/internal/runtime"
	"github.com/QuanTuanHuy/g-gateway/internal/tunnel"
	"github.com/QuanTuanHuy/g-gateway/internal/upstream"
)

const proxyWebSocketKey = "dGhlIHNhbXBsZSBub25jZQ=="

func TestWebSocketSuccessfulUpgradeHandsOffOpaqueTunnel(t *testing.T) {
	upstreamServer := newUpgradeTestServer(t, "", nil)
	handler, manager, tunnels, observer := newWebSocketTestHandler(t, webSocketResources(upstreamServer.URL, true))
	gateway := httptest.NewServer(requestctx.Middleware(handler))
	defer gateway.Close()

	connection, buffered, response := dialTestWebSocket(t, gateway.Listener.Addr().String())
	if response.StatusCode != http.StatusSwitchingProtocols {
		t.Fatalf("status = %d", response.StatusCode)
	}
	payload := []byte{0, 1, 2, 3, 0xff}
	if _, err := buffered.Write(payload); err != nil {
		t.Fatal(err)
	}
	if err := buffered.Flush(); err != nil {
		t.Fatal(err)
	}
	got := make([]byte, len(payload))
	if _, err := io.ReadFull(buffered, got); err != nil {
		t.Fatal(err)
	}
	if string(got) != string(payload) {
		t.Fatalf("echo = %v, want %v", got, payload)
	}
	_ = connection.Close()
	waitForTunnelStats(t, tunnels, tunnel.Stats{})
	if got := manager.UpstreamStats().LiveTunnelLeases; got != 0 {
		t.Fatalf("live tunnel leases = %d", got)
	}
	if !observer.saw("success") {
		t.Fatalf("handshake results = %v", observer.results())
	}
}

func TestWebSocketDisabledCandidateUsesOrdinaryHTTP(t *testing.T) {
	seenUpgrade := make(chan string, 1)
	upstreamServer := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		seenUpgrade <- request.Header.Get("Upgrade")
		writer.WriteHeader(http.StatusOK)
	}))
	defer upstreamServer.Close()
	handler, _, _, observer := newWebSocketTestHandler(t, webSocketResources(upstreamServer.URL, false))
	request := validProxyWebSocketRequest()
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	upstreamUpgrade := <-seenUpgrade
	if response.Code != http.StatusOK || upstreamUpgrade != "" {
		t.Fatalf("status=%d upstream upgrade=%q", response.Code, upstreamUpgrade)
	}
	if len(observer.results()) != 0 {
		t.Fatalf("disabled handshake was observed: %v", observer.results())
	}
}

func TestWebSocketMalformedEnabledCandidateReturns400(t *testing.T) {
	handler, _, _, observer := newWebSocketTestHandler(t, webSocketResources("http://127.0.0.1:1", true))
	request := validProxyWebSocketRequest()
	request.Header.Set("Sec-WebSocket-Key", "invalid-secret-key")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	assertErrorResponse(t, response, http.StatusBadRequest, "INVALID_WEBSOCKET_HANDSHAKE", "invalid WebSocket handshake")
	if strings.Contains(response.Body.String(), "invalid-secret-key") || !observer.saw("invalid_request") {
		t.Fatalf("response=%q observations=%v", response.Body.String(), observer.results())
	}
}

func TestWebSocketRequestPluginCannotMutateHandshakeControls(t *testing.T) {
	resources := webSocketResources("http://127.0.0.1:1", true)
	resources.Routes[0].Plugins = []model.PluginAttachment{{
		Name: "header-rewrite", Enabled: true,
		RawConfig: json.RawMessage(`{"request":{"set":{"Sec-WebSocket-Key":"AAAAAAAAAAAAAAAAAAAAAA=="}}}`),
	}}
	handler, _, _, observer := newWebSocketTestHandler(t, resources)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, validProxyWebSocketRequest())
	assertErrorResponse(t, response, http.StatusInternalServerError, "PLUGIN_REQUEST_FAILED", "request plugin failed")
	if !observer.saw("plugin_failure") {
		t.Fatalf("handshake results = %v", observer.results())
	}
}

func TestWebSocketNon101ResponseStreamsThroughResponsePlugins(t *testing.T) {
	upstreamServer := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set("Connection", "X-Remove")
		writer.Header().Set("X-Remove", "gone")
		writer.Header().Set("X-Upstream", "kept")
		writer.WriteHeader(http.StatusTeapot)
		_, _ = io.WriteString(writer, "rejected")
	}))
	defer upstreamServer.Close()
	resources := webSocketResources(upstreamServer.URL, true)
	resources.Routes[0].Plugins = []model.PluginAttachment{{
		Name: "header-rewrite", Enabled: true,
		RawConfig: json.RawMessage(`{"response":{"set":{"X-Plugin":"ran"}}}`),
	}}
	handler, _, _, observer := newWebSocketTestHandler(t, resources)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, validProxyWebSocketRequest())
	if response.Code != http.StatusTeapot || response.Body.String() != "rejected" ||
		response.Header().Get("X-Plugin") != "ran" || response.Header().Get("X-Remove") != "" {
		t.Fatalf("response=%d %#v %q", response.Code, response.Header(), response.Body.String())
	}
	if !observer.saw("upstream_rejected") {
		t.Fatalf("handshake results = %v", observer.results())
	}
}

func TestWebSocketInvalidUpstream101Returns502(t *testing.T) {
	upstreamServer := newUpgradeTestServer(t, "invalid", nil)
	handler, _, _, observer := newWebSocketTestHandler(t, webSocketResources(upstreamServer.URL, true))
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, validProxyWebSocketRequest())
	assertErrorResponse(t, response, http.StatusBadGateway, "UPSTREAM_WEBSOCKET_HANDSHAKE_INVALID", "upstream WebSocket handshake invalid")
	if !observer.saw("upstream_failure") {
		t.Fatalf("handshake results = %v", observer.results())
	}
}

func TestWebSocketRetriesInvalid101ThenSucceeds(t *testing.T) {
	var attempts atomic.Int32
	serverHandler := func(writer http.ResponseWriter, request *http.Request) {
		connection, buffered, err := http.NewResponseController(writer).Hijack()
		if err != nil {
			t.Errorf("upstream Hijack() error = %v", err)
			return
		}
		defer connection.Close()
		accept := "invalid"
		if attempts.Add(1) == 2 {
			accept = proxyWebSocketAccept(request.Header.Get("Sec-WebSocket-Key"))
		}
		if _, err := fmt.Fprintf(buffered, "HTTP/1.1 101 Switching Protocols\r\nConnection: Upgrade\r\nUpgrade: websocket\r\nSec-WebSocket-Accept: %s\r\n\r\n", accept); err != nil {
			return
		}
		if err := buffered.Flush(); err != nil || accept == "invalid" {
			return
		}
		_, _ = io.Copy(connection, connection)
	}
	first := httptest.NewServer(http.HandlerFunc(serverHandler))
	second := httptest.NewServer(http.HandlerFunc(serverHandler))
	defer first.Close()
	defer second.Close()
	resources := webSocketResources(first.URL, true)
	resources.Upstreams[0].Endpoints = append(resources.Upstreams[0].Endpoints, model.Endpoint{URL: second.URL, Weight: 1})
	resources.Upstreams[0].Retry.MaxAttempts = 2
	handler, _, _, observer := newWebSocketTestHandler(t, resources)
	gateway := httptest.NewServer(requestctx.Middleware(handler))
	defer gateway.Close()

	connection, _, response := dialTestWebSocket(t, gateway.Listener.Addr().String())
	defer connection.Close()
	waitForWebSocketObservation(t, observer, "success")
	if response.StatusCode != http.StatusSwitchingProtocols || attempts.Load() != 2 {
		t.Fatalf("status=%d attempts=%d observations=%v", response.StatusCode, attempts.Load(), observer.results())
	}
}

func TestWebSocketResponsePluginCannotMutateHandshakeControls(t *testing.T) {
	upstreamServer := newUpgradeTestServer(t, "", nil)
	resources := webSocketResources(upstreamServer.URL, true)
	resources.Routes[0].Plugins = []model.PluginAttachment{{
		Name: "header-rewrite", Enabled: true,
		RawConfig: json.RawMessage(`{"response":{"set":{"Sec-WebSocket-Accept":"changed"}}}`),
	}}
	handler, _, _, observer := newWebSocketTestHandler(t, resources)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, validProxyWebSocketRequest())
	assertErrorResponse(t, response, http.StatusInternalServerError, "PLUGIN_RESPONSE_FAILED", "response plugin failed")
	if !observer.saw("plugin_failure") {
		t.Fatalf("handshake results = %v", observer.results())
	}
}

func TestWebSocketClosedAdmissionReturns503(t *testing.T) {
	upstreamServer := newUpgradeTestServer(t, "", nil)
	handler, _, tunnels, observer := newWebSocketTestHandler(t, webSocketResources(upstreamServer.URL, true))
	tunnels.CloseAdmission()
	gateway := httptest.NewServer(requestctx.Middleware(handler))
	defer gateway.Close()
	connection, _, response := dialTestWebSocket(t, gateway.Listener.Addr().String())
	defer connection.Close()
	if response.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("status=%d", response.StatusCode)
	}
	if !observer.saw("draining") {
		t.Fatalf("handshake results = %v", observer.results())
	}
}

func TestWebSocketHijackFailureReturns500(t *testing.T) {
	upstreamServer := newUpgradeTestServer(t, "", nil)
	handler, _, _, observer := newWebSocketTestHandler(t, webSocketResources(upstreamServer.URL, true))
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, validProxyWebSocketRequest())
	assertErrorResponse(t, response, http.StatusInternalServerError, "DOWNSTREAM_HIJACK_UNSUPPORTED", "downstream hijack unsupported")
	if !observer.saw("upstream_failure") {
		t.Fatalf("handshake results = %v", observer.results())
	}
}

func webSocketResources(endpoint string, enabled bool) model.ResourceSet {
	return model.ResourceSet{
		Routes: []model.Route{{
			ID: "events", UpstreamRef: "events", WebSocket: model.WebSocketPolicyOverride{Enabled: &enabled},
			Match: model.RouteMatch{Path: "/events", Methods: []string{http.MethodGet}},
		}},
		Upstreams: []model.Upstream{{
			ID: "events", Endpoints: []model.Endpoint{{URL: endpoint, Weight: 1}},
			Balancer: model.BalancerPolicy{Type: model.BalancerWeightedRoundRobin},
			Transport: model.TransportConfig{
				Protocol: model.TransportProtocolHTTP1, DialTimeout: time.Second,
				ResponseHeaderTimeout: time.Second, IdleConnectionTimeout: time.Second,
				MaxIdleConnections: 8, MaxIdleConnectionsPerHost: 8,
			},
			Retry: model.RetryPolicy{
				MaxAttempts: 1, Methods: []string{http.MethodGet},
				RetryOn:      model.RetryOnPolicy{ConnectFailure: true, ConnectionFailure: true, ResponseHeaderTimeout: true},
				Budget:       model.RetryBudgetPolicy{RatioPer1000: 1000, Burst: 10, MaxInflight: 32},
				TotalTimeout: 2 * time.Second,
			},
		}},
	}
}

func newWebSocketTestHandler(t testing.TB, resources model.ResourceSet) (http.Handler, *gatewayruntime.Manager, *tunnel.Registry, *recordingWebSocketObserver) {
	t.Helper()
	upstreamRegistry, err := upstream.NewRegistry(upstream.RegistryOptions{MaxRetiredSnapshots: 64, HealthWorkers: 2, HealthQueueCapacity: 16})
	if err != nil {
		t.Fatal(err)
	}
	plugins, err := plugin.NewBuiltinRegistry()
	if err != nil {
		t.Fatal(err)
	}
	builder, err := gatewayruntime.NewBuilder(plugins)
	if err != nil {
		t.Fatal(err)
	}
	manager := gatewayruntime.NewManager(builder, upstreamRegistry, nil)
	if err := manager.Apply(1, resources); err != nil {
		t.Fatal(err)
	}
	observer := new(recordingWebSocketObserver)
	tunnels := tunnel.NewRegistry(nil)
	handler, err := NewRuntime(RuntimeOptions{
		Snapshots: manager, MaxRequestBodyBytes: 1 << 20,
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)), Tunnels: tunnels, WebSockets: observer,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		tunnels.CloseAdmission()
		if err := tunnels.Drain(ctx); err != nil {
			t.Errorf("tunnel Drain() error = %v", err)
		}
		if err := manager.Close(ctx); err != nil {
			t.Errorf("manager Close() error = %v", err)
		}
	})
	return requestctx.Middleware(handler), manager, tunnels, observer
}

func newUpgradeTestServer(t testing.TB, acceptOverride string, mutate func(http.Header)) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		connection, buffered, err := http.NewResponseController(writer).Hijack()
		if err != nil {
			t.Errorf("upstream Hijack() error = %v", err)
			return
		}
		defer connection.Close()
		accept := proxyWebSocketAccept(request.Header.Get("Sec-WebSocket-Key"))
		if acceptOverride != "" {
			accept = acceptOverride
		}
		header := make(http.Header)
		header.Set("Connection", "Upgrade")
		header.Set("Upgrade", "websocket")
		header.Set("Sec-WebSocket-Accept", accept)
		if mutate != nil {
			mutate(header)
		}
		if _, err := fmt.Fprintf(buffered, "HTTP/1.1 101 Switching Protocols\r\n%s\r\n", headerString(header)); err != nil {
			return
		}
		if err := buffered.Flush(); err != nil {
			return
		}
		_, _ = io.Copy(connection, connection)
	}))
	t.Cleanup(server.Close)
	return server
}

func headerString(header http.Header) string {
	var builder strings.Builder
	for name, values := range header {
		for _, value := range values {
			fmt.Fprintf(&builder, "%s: %s\r\n", name, value)
		}
	}
	return builder.String()
}

func proxyWebSocketAccept(key string) string {
	digest := sha1.Sum([]byte(key + "258EAFA5-E914-47DA-95CA-C5AB0DC85B11")) // #nosec G401 -- RFC fixture.
	return base64.StdEncoding.EncodeToString(digest[:])
}

func validProxyWebSocketRequest() *http.Request {
	request := httptest.NewRequest(http.MethodGet, "http://gateway.test/events", nil)
	request.Header.Set("Connection", "Upgrade")
	request.Header.Set("Upgrade", "websocket")
	request.Header.Set("Sec-WebSocket-Version", "13")
	request.Header.Set("Sec-WebSocket-Key", proxyWebSocketKey)
	return request
}

func dialTestWebSocket(t testing.TB, address string) (net.Conn, *bufio.ReadWriter, *http.Response) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	var connection net.Conn
	var err error
	for time.Now().Before(deadline) {
		connection, err = net.DialTimeout("tcp", address, time.Second)
		if err == nil {
			break
		}
		time.Sleep(time.Millisecond)
	}
	if connection == nil {
		t.Fatal(err)
	}
	buffered := bufio.NewReadWriter(bufio.NewReader(connection), bufio.NewWriter(connection))
	if _, err := fmt.Fprintf(buffered, "GET /events HTTP/1.1\r\nHost: gateway.test\r\nConnection: Upgrade\r\nUpgrade: websocket\r\nSec-WebSocket-Version: 13\r\nSec-WebSocket-Key: %s\r\n\r\n", proxyWebSocketKey); err != nil {
		t.Fatal(err)
	}
	if err := buffered.Flush(); err != nil {
		t.Fatal(err)
	}
	response, err := http.ReadResponse(buffered.Reader, &http.Request{Method: http.MethodGet})
	if err != nil {
		t.Fatal(err)
	}
	return connection, buffered, response
}

func waitForTunnelStats(t *testing.T, registry *tunnel.Registry, want tunnel.Stats) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if registry.Stats() == want {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("tunnel stats=%+v, want %+v", registry.Stats(), want)
}

func waitForWebSocketObservation(t *testing.T, observer *recordingWebSocketObserver, want string) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if observer.saw(want) {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("handshake results = %v, want %q", observer.results(), want)
}

type recordingWebSocketObserver struct {
	mu    sync.Mutex
	items []string
}

func (observer *recordingWebSocketObserver) ObserveWebSocketHandshake(result string) {
	observer.mu.Lock()
	observer.items = append(observer.items, result)
	observer.mu.Unlock()
}

func (observer *recordingWebSocketObserver) results() []string {
	observer.mu.Lock()
	defer observer.mu.Unlock()
	return append([]string(nil), observer.items...)
}

func (observer *recordingWebSocketObserver) saw(want string) bool {
	for _, result := range observer.results() {
		if result == want {
			return true
		}
	}
	return false
}

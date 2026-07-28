package proxy

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/QuanTuanHuy/g-gateway/internal/model"
	"github.com/QuanTuanHuy/g-gateway/internal/plugin"
	"github.com/QuanTuanHuy/g-gateway/internal/requestctx"
	"github.com/QuanTuanHuy/g-gateway/internal/runtime"
	"github.com/QuanTuanHuy/g-gateway/internal/upstream"
)

func TestRequestPluginRunsBeforeConsistentHashSelection(t *testing.T) {
	handler, rewrittenValue, expectedBody := newConsistentHashHandler(t, "X-Tenant")
	request := httptest.NewRequest(http.MethodGet, "http://gateway.test/users", nil)
	recorder := httptest.NewRecorder()

	handler.ServeHTTP(recorder, request)

	if recorder.Body.String() != expectedBody {
		t.Fatalf(
			"rewritten value %q selected body %q, want %q",
			rewrittenValue,
			recorder.Body.String(),
			expectedBody,
		)
	}
}

func newConsistentHashHandler(t *testing.T, headerName string) (http.Handler, string, string) {
	t.Helper()
	servers := []*httptest.Server{
		httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
			_, _ = io.WriteString(writer, "A")
		})),
		httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
			_, _ = io.WriteString(writer, "B")
		})),
	}
	for _, server := range servers {
		t.Cleanup(server.Close)
	}
	resources := testHandlerResources(servers[0].URL, []string{http.MethodGet})
	resources.Routes[0].Match.Path = "/users"
	resources.Upstreams[0].Endpoints = []model.Endpoint{
		{URL: servers[0].URL, Weight: 1},
		{URL: servers[1].URL, Weight: 1},
	}
	resources.Upstreams[0].Balancer = model.BalancerPolicy{
		Type: model.BalancerConsistentHash,
		HashKey: model.HashKeyPolicy{Sources: []model.HashKeySource{{
			Type: model.HashSourceHeader,
			Name: headerName,
		}}},
	}

	upstreamRegistry, err := upstream.NewRegistry(upstream.RegistryOptions{MaxRetiredSnapshots: 64, HealthWorkers: 2, HealthQueueCapacity: 16})
	if err != nil {
		t.Fatal(err)
	}
	candidate, err := upstreamRegistry.Prepare(resources.Upstreams)
	if err != nil {
		t.Fatal(err)
	}
	plan, _ := candidate.Plan("baseline")
	fallbackRequest := httptest.NewRequest(http.MethodGet, "http://gateway.test/users", nil)
	fallbackSelection, err := plan.Select(fallbackRequest)
	if err != nil {
		t.Fatal(err)
	}
	var (
		rewritten string
		expected  string
	)
	for index := 0; index < 10_000; index++ {
		value := fmt.Sprintf("tenant-%d", index)
		request := httptest.NewRequest(http.MethodGet, "http://gateway.test/users", nil)
		request.Header.Set(headerName, value)
		selection, selectErr := plan.Select(request)
		if selectErr != nil {
			t.Fatal(selectErr)
		}
		if selection.EndpointID() != fallbackSelection.EndpointID() {
			rewritten = value
			if selection.Target().Host == mustParseURL(t, servers[0].URL).Host {
				expected = "A"
			} else {
				expected = "B"
			}
			break
		}
	}
	candidate.Rollback()
	if rewritten == "" {
		t.Fatal("could not find header value that differs from remote-address fallback")
	}
	resources.Routes[0].Plugins = []model.PluginAttachment{{
		Name:      "header-rewrite",
		Enabled:   true,
		RawConfig: json.RawMessage(fmt.Sprintf(`{"request":{"set":{%q:%q}}}`, headerName, rewritten)),
	}}

	pluginRegistry, err := plugin.NewBuiltinRegistry()
	if err != nil {
		t.Fatal(err)
	}
	builder, err := runtime.NewBuilder(pluginRegistry)
	if err != nil {
		t.Fatal(err)
	}
	manager := runtime.NewManager(builder, upstreamRegistry, nil)
	if err := manager.Apply(1, resources); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		if err := manager.Close(ctx); err != nil {
			t.Errorf("Manager.Close() error = %v", err)
		}
	})
	handler, err := NewRuntime(RuntimeOptions{
		Snapshots:           manager,
		MaxRequestBodyBytes: 1024,
		Logger:              slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	if err != nil {
		t.Fatal(err)
	}
	return requestctx.Middleware(handler), rewritten, expected
}

func mustParseURL(t *testing.T, raw string) *url.URL {
	t.Helper()
	parsed, err := url.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	return parsed
}

func TestRouteMatchesExactPathAndPreservesRawQuery(t *testing.T) {
	var seenQuery string
	handler := newTestHandler(t, []string{http.MethodGet}, 1024, roundTripFunc(func(request *http.Request) (*http.Response, error) {
		seenQuery = request.URL.RawQuery
		return response(http.StatusOK, "proxied"), nil
	}))

	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "http://gateway/hello?x=1&x=%2F", nil))

	if recorder.Code != http.StatusOK || recorder.Body.String() != "proxied" {
		t.Fatalf("response = %d %q", recorder.Code, recorder.Body.String())
	}
	if seenQuery != "x=1&x=%2F" {
		t.Fatalf("upstream raw query = %q", seenQuery)
	}

	notExact := httptest.NewRecorder()
	handler.ServeHTTP(notExact, httptest.NewRequest(http.MethodGet, "http://gateway/hello/", nil))
	assertErrorResponse(t, notExact, http.StatusNotFound, "ROUTE_NOT_FOUND", "route not found")
}

func TestRouteNotFound(t *testing.T) {
	handler := newTestHandler(t, []string{http.MethodGet}, 1024, roundTripFunc(unexpectedRoundTrip(t)))
	recorder := httptest.NewRecorder()

	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "http://gateway/missing", nil))

	assertErrorResponse(t, recorder, http.StatusNotFound, "ROUTE_NOT_FOUND", "route not found")
}

func TestMethodNotAllowedIncludesSortedAllow(t *testing.T) {
	handler := newTestHandler(t, []string{http.MethodPost, http.MethodGet}, 1024, roundTripFunc(unexpectedRoundTrip(t)))
	recorder := httptest.NewRecorder()

	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodDelete, "http://gateway/hello", nil))

	assertErrorResponse(t, recorder, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "method not allowed")
	if got := recorder.Header().Get("Allow"); got != "GET, POST" {
		t.Fatalf("Allow = %q, want GET, POST", got)
	}
}

func TestInvalidRequestTarget(t *testing.T) {
	handler := newTestHandler(t, []string{http.MethodGet}, 1024, roundTripFunc(unexpectedRoundTrip(t)))
	request := httptest.NewRequest(http.MethodGet, "http://gateway/hello", nil)
	request.URL.Path = "relative"
	recorder := httptest.NewRecorder()

	handler.ServeHTTP(recorder, request)

	assertErrorResponse(t, recorder, http.StatusBadRequest, "INVALID_REQUEST", "invalid request")
}

func TestBodyOverLimit(t *testing.T) {
	handler := newTestHandler(t, []string{http.MethodPost}, 4, roundTripFunc(func(request *http.Request) (*http.Response, error) {
		_, err := io.Copy(io.Discard, request.Body)
		if err != nil {
			return nil, err
		}
		return response(http.StatusOK, "unexpected"), nil
	}))
	recorder := httptest.NewRecorder()

	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "http://gateway/hello", bytes.NewBufferString("12345")))

	assertErrorResponse(t, recorder, http.StatusRequestEntityTooLarge, "REQUEST_BODY_TOO_LARGE", "request body too large")
}

func TestUpgradeNotSupported(t *testing.T) {
	handler := newTestHandler(t, []string{http.MethodGet}, 1024, roundTripFunc(unexpectedRoundTrip(t)))
	tests := []struct {
		name       string
		connection string
		upgrade    string
	}{
		{name: "Upgrade header", upgrade: "websocket"},
		{name: "Connection token", connection: "keep-alive, Upgrade"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodGet, "http://gateway/hello", nil)
			request.Header.Set("Connection", tt.connection)
			request.Header.Set("Upgrade", tt.upgrade)
			recorder := httptest.NewRecorder()

			handler.ServeHTTP(recorder, request)

			assertErrorResponse(t, recorder, http.StatusNotImplemented, "UPGRADE_NOT_SUPPORTED", "upgrade not supported")
		})
	}
}

func TestStreamsRequestWithoutWholeBodyBuffer(t *testing.T) {
	firstChunk := make(chan struct{})
	transportDone := make(chan struct{})
	handler := newTestHandler(t, []string{http.MethodPost}, 1024, roundTripFunc(func(request *http.Request) (*http.Response, error) {
		buffer := make([]byte, 5)
		if _, err := io.ReadFull(request.Body, buffer); err != nil {
			return nil, err
		}
		close(firstChunk)
		if _, err := io.Copy(io.Discard, request.Body); err != nil {
			return nil, err
		}
		close(transportDone)
		return response(http.StatusOK, "ok"), nil
	}))
	reader, writer := io.Pipe()
	request := httptest.NewRequest(http.MethodPost, "http://gateway/hello", reader)
	request.ContentLength = -1
	recorder := httptest.NewRecorder()
	handlerDone := make(chan struct{})
	go func() {
		handler.ServeHTTP(recorder, request)
		close(handlerDone)
	}()

	if _, err := writer.Write([]byte("first")); err != nil {
		t.Fatal(err)
	}
	select {
	case <-firstChunk:
	case <-time.After(time.Second):
		t.Fatal("upstream did not receive first chunk before request completion")
	}
	if _, err := writer.Write([]byte("second")); err != nil {
		t.Fatal(err)
	}
	_ = writer.Close()

	select {
	case <-transportDone:
	case <-time.After(time.Second):
		t.Fatal("upstream did not finish reading streamed request")
	}
	select {
	case <-handlerDone:
	case <-time.After(time.Second):
		t.Fatal("proxy handler did not return")
	}
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%q", recorder.Code, recorder.Body.String())
	}
}

func TestFirstResponseChunkArrivesBeforeCompletion(t *testing.T) {
	reader, writer := io.Pipe()
	handler := newTestHandler(t, []string{http.MethodGet}, 1024, roundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     make(http.Header),
			Body:       reader,
		}, nil
	}))
	responseWriter := newObservingResponseWriter()
	request := httptest.NewRequest(http.MethodGet, "http://gateway/hello", nil)
	handlerDone := make(chan struct{})
	go func() {
		handler.ServeHTTP(responseWriter, request)
		close(handlerDone)
	}()

	if _, err := writer.Write([]byte("first")); err != nil {
		t.Fatal(err)
	}
	select {
	case <-responseWriter.firstWrite:
	case <-time.After(time.Second):
		t.Fatal("downstream did not receive first chunk before upstream completion")
	}
	if got := responseWriter.bodyString(); got != "first" {
		t.Fatalf("first downstream body = %q", got)
	}
	if _, err := writer.Write([]byte("second")); err != nil {
		t.Fatal(err)
	}
	_ = writer.Close()
	select {
	case <-handlerDone:
	case <-time.After(time.Second):
		t.Fatal("proxy handler did not finish streamed response")
	}
	if got := responseWriter.bodyString(); got != "firstsecond" {
		t.Fatalf("downstream body = %q", got)
	}
}

func TestCancellationReachesUpstream(t *testing.T) {
	started := make(chan struct{})
	canceled := make(chan struct{})
	handler := newTestHandler(t, []string{http.MethodGet}, 1024, roundTripFunc(func(request *http.Request) (*http.Response, error) {
		close(started)
		<-request.Context().Done()
		close(canceled)
		return nil, request.Context().Err()
	}))
	ctx, cancel := context.WithCancel(context.Background())
	request := httptest.NewRequest(http.MethodGet, "http://gateway/hello", nil).WithContext(ctx)
	handlerDone := make(chan struct{})
	go func() {
		handler.ServeHTTP(httptest.NewRecorder(), request)
		close(handlerDone)
	}()

	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("upstream request did not start")
	}
	cancel()
	select {
	case <-canceled:
	case <-time.After(time.Second):
		t.Fatal("upstream did not observe cancellation")
	}
	select {
	case <-handlerDone:
	case <-time.After(time.Second):
		t.Fatal("handler did not return after cancellation")
	}
}

func TestForwardsTrailers(t *testing.T) {
	handler := newTestHandler(t, []string{http.MethodGet}, 1024, roundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader("body")),
			Trailer: http.Header{
				"X-Checksum": []string{"abc123"},
			},
		}, nil
	}))
	recorder := httptest.NewRecorder()

	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "http://gateway/hello", nil))

	if got := recorder.Result().Trailer.Get("X-Checksum"); got != "abc123" {
		t.Fatalf("trailer X-Checksum = %q", got)
	}
}

func TestConnectFailureIs502(t *testing.T) {
	handler := newTestHandler(t, []string{http.MethodGet}, 1024, roundTripFunc(func(*http.Request) (*http.Response, error) {
		return nil, errors.New("dial tcp 192.0.2.1: connection refused")
	}))
	recorder := httptest.NewRecorder()

	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "http://gateway/hello", nil))

	assertErrorResponse(t, recorder, http.StatusBadGateway, "UPSTREAM_CONNECTION_FAILED", "upstream connection failed")
}

func TestResponseHeaderTimeoutIs504(t *testing.T) {
	handler := newTestHandler(t, []string{http.MethodGet}, 1024, roundTripFunc(func(*http.Request) (*http.Response, error) {
		return nil, context.DeadlineExceeded
	}))
	recorder := httptest.NewRecorder()

	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "http://gateway/hello", nil))

	assertErrorResponse(t, recorder, http.StatusGatewayTimeout, "UPSTREAM_TIMEOUT", "upstream timeout")
}

func TestUpstreamResetBeforeHeadersIs502(t *testing.T) {
	handler := newTestHandler(t, []string{http.MethodGet}, 1024, roundTripFunc(func(*http.Request) (*http.Response, error) {
		return nil, errors.New("connection reset by peer")
	}))
	recorder := httptest.NewRecorder()

	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "http://gateway/hello", nil))

	assertErrorResponse(t, recorder, http.StatusBadGateway, "UPSTREAM_CONNECTION_FAILED", "upstream connection failed")
}

func TestUpstreamErrorLogsAreRateLimited(t *testing.T) {
	var logs bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logs, nil))
	handler := newTestHandlerWithLogger(
		t,
		[]string{http.MethodGet},
		1024,
		roundTripFunc(func(*http.Request) (*http.Response, error) {
			return nil, errors.New("upstream unavailable")
		}),
		logger,
	)

	for range 5 {
		handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "http://gateway/hello", nil))
	}
	if got := strings.Count(logs.String(), "upstream request failed"); got != 1 {
		t.Fatalf("log count = %d, want 1; logs=%q", got, logs.String())
	}
}

func newTestHandler(t *testing.T, methods []string, maxBody int64, transport http.RoundTripper) http.Handler {
	t.Helper()
	return newTestHandlerWithLogger(
		t,
		methods,
		maxBody,
		transport,
		slog.New(slog.NewTextHandler(io.Discard, nil)),
	)
}

func newTestHandlerWithLogger(
	t *testing.T,
	methods []string,
	maxBody int64,
	transport http.RoundTripper,
	logger *slog.Logger,
) http.Handler {
	t.Helper()
	upstreamServer := httptest.NewServer(roundTripperAdapter(transport))
	t.Cleanup(upstreamServer.Close)
	resources := testHandlerResources(upstreamServer.URL, methods)
	upstreamRegistry, err := upstream.NewRegistry(upstream.RegistryOptions{MaxRetiredSnapshots: 64, HealthWorkers: 2, HealthQueueCapacity: 16})
	if err != nil {
		t.Fatal(err)
	}
	pluginRegistry, err := plugin.NewBuiltinRegistry()
	if err != nil {
		t.Fatal(err)
	}
	builder, err := runtime.NewBuilder(pluginRegistry)
	if err != nil {
		t.Fatal(err)
	}
	manager := runtime.NewManager(builder, upstreamRegistry, nil)
	if err := manager.Apply(1, resources); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		if err := manager.Close(ctx); err != nil {
			t.Errorf("Manager.Close() error = %v", err)
		}
	})
	handler, err := NewRuntime(RuntimeOptions{
		Snapshots:           manager,
		MaxRequestBodyBytes: maxBody,
		Logger:              logger,
	})
	if err != nil {
		t.Fatalf("NewRuntime() error = %v", err)
	}
	return requestctx.Middleware(handler)
}

func testHandlerResources(endpoint string, methods []string) model.ResourceSet {
	return model.ResourceSet{
		Routes: []model.Route{{
			ID: "baseline",
			Match: model.RouteMatch{
				Path:    "/hello",
				Methods: append([]string(nil), methods...),
			},
			UpstreamRef: "baseline",
		}},
		Upstreams: []model.Upstream{{
			ID:        "baseline",
			Endpoints: []model.Endpoint{{URL: endpoint, Weight: 1}},
			Balancer:  model.BalancerPolicy{Type: model.BalancerWeightedRoundRobin},
			Transport: model.TransportConfig{
				DialTimeout:               time.Second,
				ResponseHeaderTimeout:     25 * time.Millisecond,
				IdleConnectionTimeout:     time.Second,
				MaxIdleConnections:        8,
				MaxIdleConnectionsPerHost: 8,
			},
		}},
	}
}

func roundTripperAdapter(transport http.RoundTripper) http.Handler {
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		response, err := transport.RoundTrip(request)
		if err != nil {
			if request.Context().Err() != nil {
				return
			}
			if errors.Is(err, context.DeadlineExceeded) {
				select {
				case <-time.After(100 * time.Millisecond):
				case <-request.Context().Done():
				}
				return
			}
			hijacker, ok := writer.(http.Hijacker)
			if !ok {
				http.Error(writer, "upstream failure", http.StatusInternalServerError)
				return
			}
			connection, _, hijackErr := hijacker.Hijack()
			if hijackErr == nil {
				_ = connection.Close()
			}
			return
		}
		if response.Body != nil {
			defer response.Body.Close()
		}
		for name, values := range response.Header {
			writer.Header()[name] = append([]string(nil), values...)
		}
		for name := range response.Trailer {
			writer.Header().Add("Trailer", name)
		}
		status := response.StatusCode
		if status == 0 {
			status = http.StatusOK
		}
		writer.WriteHeader(status)
		if response.Body != nil {
			buffer := make([]byte, 32*1024)
			for {
				count, readErr := response.Body.Read(buffer)
				if count > 0 {
					_, _ = writer.Write(buffer[:count])
					if flusher, ok := writer.(http.Flusher); ok {
						flusher.Flush()
					}
				}
				if readErr != nil {
					break
				}
			}
		}
		for name, values := range response.Trailer {
			writer.Header()[name] = append([]string(nil), values...)
		}
	})
}

func assertErrorResponse(t *testing.T, recorder *httptest.ResponseRecorder, status int, code, message string) {
	t.Helper()
	if recorder.Code != status {
		t.Fatalf("status = %d, want %d; body=%q", recorder.Code, status, recorder.Body.String())
	}
	if contentType := recorder.Header().Get("Content-Type"); contentType != "application/json" {
		t.Fatalf("Content-Type = %q, want application/json", contentType)
	}
	var body map[string]any
	if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
		t.Fatalf("invalid JSON body %q: %v", recorder.Body.String(), err)
	}
	if len(body) != 2 || body["code"] != code || body["message"] != message {
		t.Fatalf("error body = %#v, want code=%q message=%q", body, code, message)
	}
	if strings.Contains(recorder.Body.String(), "upstream:8080") {
		t.Fatalf("error leaked upstream address: %q", recorder.Body.String())
	}
}

func unexpectedRoundTrip(t *testing.T) func(*http.Request) (*http.Response, error) {
	t.Helper()
	return func(*http.Request) (*http.Response, error) {
		t.Error("RoundTrip was called unexpectedly")
		return nil, errors.New("unexpected RoundTrip")
	}
}

func response(status int, body string) *http.Response {
	return &http.Response{
		StatusCode: status,
		Header:     make(http.Header),
		Body:       io.NopCloser(strings.NewReader(body)),
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (fn roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return fn(request)
}

type observingResponseWriter struct {
	mu         sync.Mutex
	header     http.Header
	status     int
	body       bytes.Buffer
	firstWrite chan struct{}
	wroteFirst bool
}

func newObservingResponseWriter() *observingResponseWriter {
	return &observingResponseWriter{
		header:     make(http.Header),
		firstWrite: make(chan struct{}),
	}
}

func (w *observingResponseWriter) Header() http.Header {
	return w.header
}

func (w *observingResponseWriter) WriteHeader(status int) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.status == 0 {
		w.status = status
	}
}

func (w *observingResponseWriter) Write(data []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.status == 0 {
		w.status = http.StatusOK
	}
	n, err := w.body.Write(data)
	if !w.wroteFirst {
		w.wroteFirst = true
		close(w.firstWrite)
	}
	return n, err
}

func (w *observingResponseWriter) Flush() {}

func (w *observingResponseWriter) bodyString() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.body.String()
}

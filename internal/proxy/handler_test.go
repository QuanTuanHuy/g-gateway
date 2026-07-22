package proxy

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
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
)

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
	target, _ := url.Parse("http://upstream:8080")
	handler, err := New(Options{
		Route:  model.Route{ID: "baseline", Match: model.RouteMatch{Path: "/hello", Methods: []string{http.MethodGet}}},
		Target: target,
		Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
			return nil, errors.New("upstream unavailable")
		}),
		MaxRequestBodyBytes: 1024,
		Logger:              logger,
	})
	if err != nil {
		t.Fatal(err)
	}

	for range 5 {
		handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "http://gateway/hello", nil))
	}
	if got := strings.Count(logs.String(), "upstream request failed"); got != 1 {
		t.Fatalf("log count = %d, want 1; logs=%q", got, logs.String())
	}
}

func newTestHandler(t *testing.T, methods []string, maxBody int64, transport http.RoundTripper) http.Handler {
	t.Helper()
	target, err := url.Parse("http://upstream:8080")
	if err != nil {
		t.Fatal(err)
	}
	handler, err := New(Options{
		Route: model.Route{
			ID: "baseline",
			Match: model.RouteMatch{
				Path:    "/hello",
				Methods: methods,
			},
			UpstreamRef: "baseline",
		},
		Target:              target,
		Transport:           transport,
		MaxRequestBodyBytes: maxBody,
		Logger:              slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	return handler
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

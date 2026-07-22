package upstream

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/QuanTuanHuy/g-gateway/internal/model"
)

func TestNewBuildsHTTP1OnlyTransport(t *testing.T) {
	runtime, err := New(testResource("http://upstream:8080"))
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	transport, ok := runtime.RoundTripper().(*http.Transport)
	if !ok {
		t.Fatalf("RoundTripper() type = %T, want *http.Transport", runtime.RoundTripper())
	}
	if transport.Protocols == nil || !transport.Protocols.HTTP1() || transport.Protocols.HTTP2() {
		t.Fatalf("Protocols = %+v, want HTTP/1 only", transport.Protocols)
	}
	if transport.ForceAttemptHTTP2 {
		t.Fatal("ForceAttemptHTTP2 = true, want false")
	}

	target := runtime.Target()
	if target.String() != "http://upstream:8080" {
		t.Fatalf("Target() = %q", target)
	}
	target.Host = "mutated:9999"
	if runtime.Target().Host != "upstream:8080" {
		t.Fatalf("Target() exposed mutable state: %q", runtime.Target())
	}
}

func TestRuntimeReusesConnections(t *testing.T) {
	var newConnections atomic.Int64
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "ok")
	}))
	server.Config.ConnState = func(_ net.Conn, state http.ConnState) {
		if state == http.StateNew {
			newConnections.Add(1)
		}
	}
	server.Start()
	defer server.Close()

	runtime, err := New(testResource(server.URL))
	if err != nil {
		t.Fatal(err)
	}
	defer runtime.CloseIdleConnections()

	for range 2 {
		response, err := runtime.RoundTripper().RoundTrip(mustRequest(t, context.Background(), server.URL))
		if err != nil {
			t.Fatalf("RoundTrip() error = %v", err)
		}
		_, _ = io.Copy(io.Discard, response.Body)
		_ = response.Body.Close()
	}

	if got := newConnections.Load(); got != 1 {
		t.Fatalf("new connections = %d, want 1", got)
	}
}

func TestRuntimeHonorsResponseHeaderTimeout(t *testing.T) {
	release := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		<-release
		w.WriteHeader(http.StatusNoContent)
	}))
	defer func() {
		close(release)
		server.Close()
	}()

	resource := testResource(server.URL)
	resource.Transport.ResponseHeaderTimeout = 30 * time.Millisecond
	runtime, err := New(resource)
	if err != nil {
		t.Fatal(err)
	}
	defer runtime.CloseIdleConnections()

	_, err = runtime.RoundTripper().RoundTrip(mustRequest(t, context.Background(), server.URL))
	if err == nil {
		t.Fatal("RoundTrip() error = nil, want response-header timeout")
	}
	var netError net.Error
	if !errors.As(err, &netError) || !netError.Timeout() {
		t.Fatalf("RoundTrip() error = %T %v, want timeout net.Error", err, err)
	}
}

func TestRoundTripUsesRequestCancellation(t *testing.T) {
	started := make(chan struct{})
	canceled := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, request *http.Request) {
		close(started)
		<-request.Context().Done()
		close(canceled)
	}))
	defer server.Close()

	runtime, err := New(testResource(server.URL))
	if err != nil {
		t.Fatal(err)
	}
	defer runtime.CloseIdleConnections()

	ctx, cancel := context.WithCancel(context.Background())
	request := mustRequest(t, ctx, server.URL)
	result := make(chan error, 1)
	go func() {
		_, err := runtime.RoundTripper().RoundTrip(request)
		result <- err
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
		t.Fatal("upstream did not observe request cancellation")
	}
	select {
	case err := <-result:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("RoundTrip() error = %v, want context canceled", err)
		}
	case <-time.After(time.Second):
		t.Fatal("RoundTrip did not return after cancellation")
	}
}

func TestCloseIdleConnections(t *testing.T) {
	closed := make(chan struct{}, 1)
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "ok")
	}))
	server.Config.ConnState = func(_ net.Conn, state http.ConnState) {
		if state == http.StateClosed {
			select {
			case closed <- struct{}{}:
			default:
			}
		}
	}
	server.Start()
	defer server.Close()

	runtime, err := New(testResource(server.URL))
	if err != nil {
		t.Fatal(err)
	}
	response, err := runtime.RoundTripper().RoundTrip(mustRequest(t, context.Background(), server.URL))
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.Copy(io.Discard, response.Body)
	_ = response.Body.Close()

	runtime.CloseIdleConnections()
	select {
	case <-closed:
	case <-time.After(time.Second):
		t.Fatal("idle connection was not closed")
	}
}

func testResource(endpoint string) model.Upstream {
	return model.Upstream{
		ID:        "baseline",
		Endpoints: []string{endpoint},
		Transport: model.TransportConfig{
			DialTimeout:               time.Second,
			ResponseHeaderTimeout:     time.Second,
			IdleConnectionTimeout:     time.Minute,
			MaxIdleConnections:        32,
			MaxIdleConnectionsPerHost: 32,
		},
	}
}

func mustRequest(t *testing.T, ctx context.Context, target string) *http.Request {
	t.Helper()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		t.Fatal(err)
	}
	return request
}

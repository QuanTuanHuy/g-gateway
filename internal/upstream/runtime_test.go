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
	runtime := newTransportRuntime(testTransportProfile(t, "http://upstream:8080"))
	endpoint, err := newEndpointRuntime("baseline", testResource("http://upstream:8080").Endpoints[0])
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	transport := runtime.production
	if transport.Protocols == nil || !transport.Protocols.HTTP1() || transport.Protocols.HTTP2() {
		t.Fatalf("Protocols = %+v, want HTTP/1 only", transport.Protocols)
	}
	if transport.ForceAttemptHTTP2 {
		t.Fatal("ForceAttemptHTTP2 = true, want false")
	}

	if endpoint.target.String() != "http://upstream:8080" {
		t.Fatalf("target = %q", endpoint.target)
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

	runtime := newTransportRuntime(testTransportProfile(t, server.URL))
	defer runtime.CloseIdleConnections()

	for range 2 {
		response, err := runtime.RoundTrip(mustRequest(t, context.Background(), server.URL))
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
	runtime := newTransportRuntime(testTransportProfileForResource(t, resource))
	defer runtime.CloseIdleConnections()

	_, err := runtime.RoundTrip(mustRequest(t, context.Background(), server.URL))
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

	runtime := newTransportRuntime(testTransportProfile(t, server.URL))
	defer runtime.CloseIdleConnections()

	ctx, cancel := context.WithCancel(context.Background())
	request := mustRequest(t, ctx, server.URL)
	result := make(chan error, 1)
	go func() {
		_, err := runtime.RoundTrip(request)
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

	runtime := newTransportRuntime(testTransportProfile(t, server.URL))
	response, err := runtime.RoundTrip(mustRequest(t, context.Background(), server.URL))
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
		Endpoints: []model.Endpoint{{URL: endpoint, Weight: 1}},
		Balancer:  model.BalancerPolicy{Type: model.BalancerWeightedRoundRobin},
		Transport: model.TransportConfig{
			DialTimeout:               time.Second,
			ResponseHeaderTimeout:     time.Second,
			IdleConnectionTimeout:     time.Minute,
			MaxIdleConnections:        32,
			MaxIdleConnectionsPerHost: 32,
		},
	}
}

func testTransportProfile(t *testing.T, endpoint string) transportProfile {
	t.Helper()
	return testTransportProfileForResource(t, testResource(endpoint))
}

func testTransportProfileForResource(t *testing.T, resource model.Upstream) transportProfile {
	t.Helper()
	profile, err := compileTransportProfile(resource, materialIndex{})
	if err != nil {
		t.Fatal(err)
	}
	return profile
}

func mustRequest(t *testing.T, ctx context.Context, target string) *http.Request {
	t.Helper()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		t.Fatal(err)
	}
	return request
}

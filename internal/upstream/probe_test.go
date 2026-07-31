package upstream

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/QuanTuanHuy/g-gateway/internal/model"
)

func TestHTTPProberClassifiesConfiguredStatusesWithoutFollowingRedirects(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/redirect":
			http.Redirect(writer, request, "/healthy", http.StatusFound)
		case "/unhealthy":
			writer.WriteHeader(http.StatusServiceUnavailable)
			_, _ = writer.Write([]byte(strings.Repeat("x", 8<<10)))
		default:
			writer.WriteHeader(http.StatusOK)
		}
	}))
	defer server.Close()
	targetURL, _ := url.Parse(server.URL)
	prober := newHTTPProber()
	defer prober.CloseIdleConnections()
	transport := http.DefaultTransport.(*http.Transport).Clone()
	defer transport.CloseIdleConnections()

	tests := []struct {
		path       string
		wantKind   OutcomeKind
		wantStatus int
	}{
		{path: "/healthy", wantKind: OutcomeSuccess, wantStatus: 200},
		{path: "/unhealthy", wantKind: OutcomeHTTPFailure, wantStatus: 503},
		{path: "/redirect", wantKind: OutcomeNeutral, wantStatus: 302},
	}
	for _, test := range tests {
		t.Run(test.path, func(t *testing.T) {
			result := prober.Probe(context.Background(), ProbeTarget{
				EndpointID: "users\x00" + server.URL,
				URL:        targetURL,
				Generation: 1,
				Transport:  transport,
				Policy: model.ActiveHealthPolicy{
					Type:              model.HealthCheckHTTP,
					Timeout:           time.Second,
					Path:              test.path,
					HealthyStatuses:   []uint16{200},
					UnhealthyStatuses: []uint16{503},
				},
			})
			if result.Observation.Source != SourceActive ||
				result.Observation.Kind != test.wantKind ||
				result.Observation.Status != test.wantStatus {
				t.Fatalf("observation = %+v", result.Observation)
			}
		})
	}
}

func TestHTTPProberClassifiesDeadline(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		time.Sleep(100 * time.Millisecond)
	}))
	defer server.Close()
	targetURL, _ := url.Parse(server.URL)
	transport := http.DefaultTransport.(*http.Transport).Clone()
	defer transport.CloseIdleConnections()
	result := newHTTPProber().Probe(context.Background(), ProbeTarget{
		URL:       targetURL,
		Transport: transport,
		Policy: model.ActiveHealthPolicy{
			Type:    model.HealthCheckHTTP,
			Timeout: 10 * time.Millisecond,
			Path:    "/",
		},
	})
	if result.Observation.Kind != OutcomeTimeout {
		t.Fatalf("observation = %+v, want timeout", result.Observation)
	}
}

func TestHTTPProberUsesOnlyTargetTransportAndCapsBodyDrain(t *testing.T) {
	firstBody := &countingReadCloser{reader: strings.NewReader(strings.Repeat("a", 8<<10))}
	first := &countingRoundTripper{
		response: &http.Response{
			StatusCode: http.StatusOK,
			Header:     make(http.Header),
			Body:       firstBody,
		},
	}
	second := &countingRoundTripper{
		response: &http.Response{
			StatusCode: http.StatusFound,
			Header:     make(http.Header),
			Body:       http.NoBody,
		},
	}
	targetURL := mustURL(t, "http://probe.example")
	prober := newHTTPProber()
	policy := model.ActiveHealthPolicy{
		Type:              model.HealthCheckHTTP,
		Timeout:           time.Second,
		Path:              "/health",
		HealthyStatuses:   []uint16{200},
		UnhealthyStatuses: []uint16{503},
	}

	firstResult := prober.Probe(context.Background(), ProbeTarget{
		URL: targetURL, Transport: first, Policy: policy,
	})
	secondResult := prober.Probe(context.Background(), ProbeTarget{
		URL: targetURL, Transport: second, Policy: policy,
	})

	if first.calls != 1 || second.calls != 1 {
		t.Fatalf("transport calls first=%d second=%d", first.calls, second.calls)
	}
	if firstResult.Observation.Kind != OutcomeSuccess ||
		secondResult.Observation.Kind != OutcomeNeutral ||
		secondResult.Observation.Status != http.StatusFound {
		t.Fatalf("results first=%+v second=%+v", firstResult.Observation, secondResult.Observation)
	}
	if firstBody.read != 4<<10+1 || !firstBody.closed {
		t.Fatalf("body read=%d closed=%v", firstBody.read, firstBody.closed)
	}
}

func TestHTTPProberClassifiesCancellationTLSAndNilTransport(t *testing.T) {
	targetURL := mustURL(t, "https://probe.example")
	policy := model.ActiveHealthPolicy{
		Type:    model.HealthCheckHTTP,
		Timeout: 10 * time.Millisecond,
		Path:    "/",
	}
	prober := newHTTPProber()

	canceling := roundTripFunc(func(request *http.Request) (*http.Response, error) {
		<-request.Context().Done()
		return nil, request.Context().Err()
	})
	result := prober.Probe(context.Background(), ProbeTarget{
		URL: targetURL, Transport: canceling, Policy: policy,
	})
	if result.Observation.Kind != OutcomeTimeout {
		t.Fatalf("cancellation observation=%+v", result.Observation)
	}

	tlsFailure := roundTripFunc(func(*http.Request) (*http.Response, error) {
		return nil, &TLSFailureError{Class: TLSFailureTrust, Err: x509UnknownAuthorityForProbe{}}
	})
	result = prober.Probe(context.Background(), ProbeTarget{
		URL: targetURL, Transport: tlsFailure, Policy: policy,
	})
	if result.Observation.Kind != OutcomeTransportFailure {
		t.Fatalf("TLS observation=%+v", result.Observation)
	}

	result = prober.Probe(context.Background(), ProbeTarget{
		URL: targetURL, Policy: policy,
	})
	if result.Observation.Kind != OutcomeTransportFailure {
		t.Fatalf("nil transport observation=%+v", result.Observation)
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return f(request)
}

type countingRoundTripper struct {
	calls    int
	response *http.Response
}

func (t *countingRoundTripper) RoundTrip(*http.Request) (*http.Response, error) {
	t.calls++
	return t.response, nil
}

type countingReadCloser struct {
	reader *strings.Reader
	read   int
	closed bool
}

func (b *countingReadCloser) Read(destination []byte) (int, error) {
	count, err := b.reader.Read(destination)
	b.read += count
	return count, err
}

func (b *countingReadCloser) Close() error {
	b.closed = true
	return nil
}

type x509UnknownAuthorityForProbe struct{}

func (x509UnknownAuthorityForProbe) Error() string {
	return "sensitive trust detail"
}

func TestTCPProberClassifiesSuccessFailureAndCancellation(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	targetURL := &url.URL{Scheme: "http", Host: listener.Addr().String()}
	prober := newTCPProber()
	defer prober.CloseIdleConnections()

	result := prober.Probe(context.Background(), ProbeTarget{
		URL:    targetURL,
		Policy: model.ActiveHealthPolicy{Type: model.HealthCheckTCP, Timeout: time.Second},
	})
	if result.Observation.Kind != OutcomeSuccess {
		t.Fatalf("success observation = %+v", result.Observation)
	}
	_ = listener.Close()
	result = prober.Probe(context.Background(), ProbeTarget{
		URL:    targetURL,
		Policy: model.ActiveHealthPolicy{Type: model.HealthCheckTCP, Timeout: time.Second},
	})
	if result.Observation.Kind != OutcomeTransportFailure {
		t.Fatalf("failure observation = %+v", result.Observation)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	result = prober.Probe(ctx, ProbeTarget{
		URL:    targetURL,
		Policy: model.ActiveHealthPolicy{Type: model.HealthCheckTCP, Timeout: time.Second},
	})
	if result.Observation.Kind != OutcomeTimeout {
		t.Fatalf("cancel observation = %+v", result.Observation)
	}
}

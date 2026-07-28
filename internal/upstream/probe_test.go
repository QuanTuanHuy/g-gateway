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
	result := newHTTPProber().Probe(context.Background(), ProbeTarget{
		URL: targetURL,
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

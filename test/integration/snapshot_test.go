package integration_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/QuanTuanHuy/g-gateway/internal/config"
	"github.com/QuanTuanHuy/g-gateway/internal/gateway"
	"github.com/QuanTuanHuy/g-gateway/internal/model"
)

func TestInFlightRequestKeepsOneSnapshotRevision(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	var holdOnce sync.Once
	upstreamA := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		holdOnce.Do(func() {
			close(started)
			<-release
		})
		_, _ = io.WriteString(response, "A")
	}))
	t.Cleanup(upstreamA.Close)
	upstreamB := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(response, "B")
	}))
	t.Cleanup(upstreamB.Close)

	instance, addresses := startSnapshotGateway(
		t,
		snapshotResources(upstreamA.URL, upstreamB.URL, "upstream-a", "1"),
	)
	target := "http://" + loopback(t, addresses.HTTP) + "/hello"
	type result struct {
		body     string
		revision string
		err      error
	}
	heldResult := make(chan result, 1)
	go func() {
		body, revision, err := getSnapshotResponse(context.Background(), http.DefaultClient, target)
		heldResult <- result{body: body, revision: revision, err: err}
	}()

	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("revision 1 request did not reach upstream A")
	}
	if err := instance.Apply(2, snapshotResources(upstreamA.URL, upstreamB.URL, "upstream-b", "2")); err != nil {
		t.Fatalf("Apply(2) error = %v", err)
	}
	body, revision, err := getSnapshotResponse(context.Background(), http.DefaultClient, target)
	if err != nil {
		t.Fatal(err)
	}
	if body != "B" || revision != "2" {
		t.Fatalf("new request = upstream %q revision %q, want B/2", body, revision)
	}

	close(release)
	held := <-heldResult
	if held.err != nil {
		t.Fatal(held.err)
	}
	if held.body != "A" || held.revision != "1" {
		t.Fatalf("held request = upstream %q revision %q, want A/1", held.body, held.revision)
	}
}

func TestConcurrentRequestsNeverMixSnapshotRevisions(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	upstreamA := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(response, "A")
	}))
	t.Cleanup(upstreamA.Close)
	upstreamB := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(response, "B")
	}))
	t.Cleanup(upstreamB.Close)
	instance, addresses := startSnapshotGateway(
		t,
		snapshotResources(upstreamA.URL, upstreamB.URL, "upstream-a", "A"),
	)
	target := "http://" + loopback(t, addresses.HTTP) + "/hello"
	transport := &http.Transport{MaxIdleConns: 64, MaxIdleConnsPerHost: 64}
	client := &http.Client{Transport: transport}
	t.Cleanup(transport.CloseIdleConnections)

	const workers = 32
	var requests atomic.Int64
	workerErrors := make(chan error, workers)
	startWorkers := make(chan struct{})
	stopWorkers := make(chan struct{})
	var workersReady sync.WaitGroup
	var workerWG sync.WaitGroup
	workersReady.Add(workers)
	workerWG.Add(workers)
	for range workers {
		go func() {
			defer workerWG.Done()
			workersReady.Done()
			<-startWorkers
			for {
				select {
				case <-stopWorkers:
					return
				default:
				}
				body, revision, err := getSnapshotResponse(ctx, client, target)
				if err != nil {
					if ctx.Err() == nil {
						workerErrors <- err
					}
					return
				}
				requests.Add(1)
				if body != revision || body != "A" && body != "B" {
					workerErrors <- fmt.Errorf("mixed snapshot response: upstream=%q revision=%q", body, revision)
					return
				}
			}
		}()
	}
	workersReady.Wait()
	close(startWorkers)

	for swap := 0; swap < 1000; swap++ {
		targetUpstream, marker := "upstream-a", "A"
		if swap%2 == 0 {
			targetUpstream, marker = "upstream-b", "B"
		}
		if err := instance.Apply(
			uint64(swap+2),
			snapshotResources(upstreamA.URL, upstreamB.URL, targetUpstream, marker),
		); err != nil {
			close(stopWorkers)
			workerWG.Wait()
			t.Fatalf("Apply(%d) error = %v", swap+2, err)
		}
		select {
		case err := <-workerErrors:
			close(stopWorkers)
			workerWG.Wait()
			t.Fatal(err)
		default:
		}
	}
	close(stopWorkers)
	workerWG.Wait()
	close(workerErrors)
	for err := range workerErrors {
		t.Fatal(err)
	}
	if ctx.Err() != nil {
		t.Fatalf("snapshot consistency test exceeded deadline: %v", ctx.Err())
	}
	if got := requests.Load(); got < workers {
		t.Fatalf("completed requests = %d, want at least %d", got, workers)
	}
}

func startSnapshotGateway(t *testing.T, resources model.ResourceSet) (*gateway.Gateway, gateway.Addresses) {
	t.Helper()
	certFile, keyFile := writeCertificatePair(t)
	bootstrap := config.BootstrapConfig{
		HTTP: config.ListenerConfig{Address: "127.0.0.1:0"},
		HTTPS: config.TLSListenerConfig{
			Address:         "127.0.0.1:0",
			CertificateFile: certFile,
			PrivateKeyFile:  keyFile,
		},
		Admin: config.ListenerConfig{Address: "127.0.0.1:0"},
		Runtime: config.RuntimeConfig{
			MaxRetiredSnapshots: 1024,
		},
		Server: config.ServerConfig{
			ReadHeaderTimeout:   time.Second,
			IdleTimeout:         time.Minute,
			ShutdownTimeout:     3 * time.Second,
			MaxHeaderBytes:      1 << 20,
			MaxRequestBodyBytes: 1 << 20,
		},
	}
	instance, err := gateway.New(bootstrap, resources, discardLogger())
	if err != nil {
		t.Fatalf("gateway.New() error = %v", err)
	}
	addresses, err := instance.Start()
	if err != nil {
		t.Fatalf("Gateway.Start() error = %v", err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		if err := instance.Shutdown(ctx); err != nil {
			t.Errorf("Gateway.Shutdown() error = %v", err)
		}
	})
	return instance, addresses
}

func snapshotResources(endpointA, endpointB, target, marker string) model.ResourceSet {
	transport := model.TransportConfig{
		DialTimeout:               time.Second,
		ResponseHeaderTimeout:     time.Second,
		IdleConnectionTimeout:     time.Minute,
		MaxIdleConnections:        64,
		MaxIdleConnectionsPerHost: 64,
	}
	return model.ResourceSet{
		Routes: []model.Route{{
			ID: "snapshot-route",
			Match: model.RouteMatch{
				Path:    "/hello",
				Methods: []string{http.MethodGet},
			},
			UpstreamRef: target,
			Plugins: []model.PluginAttachment{{
				Name:      "header-rewrite",
				Enabled:   true,
				RawConfig: json.RawMessage(`{"response":{"set":{"X-Revision":"` + marker + `"}}}`),
			}},
		}},
		Upstreams: []model.Upstream{
			{
				ID:        "upstream-a",
				Endpoints: []model.Endpoint{{URL: endpointA, Weight: 1}},
				Balancer:  model.BalancerPolicy{Type: model.BalancerWeightedRoundRobin},
				Transport: transport,
			},
			{
				ID:        "upstream-b",
				Endpoints: []model.Endpoint{{URL: endpointB, Weight: 1}},
				Balancer:  model.BalancerPolicy{Type: model.BalancerWeightedRoundRobin},
				Transport: transport,
			},
		},
	}
}

func getSnapshotResponse(
	ctx context.Context,
	client *http.Client,
	target string,
) (body, revision string, err error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return "", "", err
	}
	response, err := client.Do(request)
	if err != nil {
		return "", "", err
	}
	defer response.Body.Close()
	data, err := io.ReadAll(response.Body)
	if err != nil {
		return "", "", err
	}
	if response.StatusCode != http.StatusOK {
		return "", "", fmt.Errorf("GET %s status = %d, body=%q", target, response.StatusCode, data)
	}
	return string(data), response.Header.Get("X-Revision"), nil
}

package gateway

import (
	"context"
	"io"
	"net"
	"net/http"
	"runtime"
	"testing"
	"time"

	"github.com/QuanTuanHuy/g-gateway/internal/tunnel"
	"github.com/QuanTuanHuy/g-gateway/internal/upstream"
)

func TestPhase3C3GatewayLifecycle(t *testing.T) {
	const tunnels = 200
	baselineGoroutines := runtime.NumGoroutine()
	fixture := newGatewayFixture(t, http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.WriteHeader(http.StatusNoContent)
	}))
	if _, err := fixture.gateway.Start(); err != nil {
		t.Fatal(err)
	}

	peers := make([]io.Closer, 0, tunnels*2)
	for range tunnels {
		downstreamPeer, downstreamTunnel := net.Pipe()
		upstreamTunnel, upstreamPeer := net.Pipe()
		session, err := tunnel.NewSession(
			tunnel.Endpoint{Reader: downstreamTunnel, Writer: downstreamTunnel, Closer: downstreamTunnel},
			tunnel.Endpoint{Reader: upstreamTunnel, Writer: upstreamTunnel, Closer: upstreamTunnel},
			0,
		)
		if err != nil {
			t.Fatal(err)
		}
		registration, err := fixture.gateway.tunnels.Register(session, func() {})
		if err != nil {
			t.Fatal(err)
		}
		registration.Activate(context.Background())
		peers = append(peers, downstreamPeer, upstreamPeer)
	}
	if got := fixture.gateway.tunnels.Stats().Active; got != tunnels {
		t.Fatalf("active tunnels=%d, want %d", got, tunnels)
	}

	done := make(chan error, 1)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		done <- fixture.gateway.Shutdown(ctx)
	}()
	time.Sleep(20 * time.Millisecond)
	for _, peer := range peers {
		_ = peer.Close()
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("gateway shutdown did not drain tunnel profile")
	}
	if got := fixture.gateway.tunnels.Stats(); got != (tunnel.Stats{}) {
		t.Fatalf("tunnel stats after shutdown=%+v", got)
	}
	if got := fixture.gateway.manager.UpstreamStats(); got != (upstream.RegistryStats{}) {
		t.Fatalf("upstream stats after shutdown=%+v", got)
	}
	t.Logf("seed=20260731 os=%s arch=%s go=%s tunnels=%d goroutine_delta=%d", runtime.GOOS, runtime.GOARCH, runtime.Version(), tunnels, runtime.NumGoroutine()-baselineGoroutines)
}

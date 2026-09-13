package proxy

import (
	"io"
	"net/http/httptest"
	"os"
	"runtime"
	"sort"
	"testing"
	"time"

	"github.com/QuanTuanHuy/g-gateway/internal/model"
	"github.com/QuanTuanHuy/g-gateway/internal/tunnel"
	"github.com/QuanTuanHuy/g-gateway/internal/upstream"
)

type phase3C3Profile struct {
	Tunnels           int
	MessagesPerTunnel int
	Rotations         int
}

type phase3C3Measurement struct {
	throughput float64
	p99        time.Duration
}

func TestPhase3C3WebSocketLifecycle(t *testing.T) {
	profile := phase3C3Profile{Tunnels: 200, MessagesPerTunnel: 20, Rotations: 2}
	full := os.Getenv("GATEWAY_PHASE3C3_ACCEPTANCE") == "1"
	if full {
		profile = phase3C3Profile{Tunnels: 10_000, MessagesPerTunnel: 100, Rotations: 20}
	}
	const seed = 20260731
	var before runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)
	baselineGoroutines := runtime.NumGoroutine()

	upstreamServer := newUpgradeTestServer(t, "", nil)
	resources := webSocketResources(upstreamServer.URL, true)
	handler, manager, tunnels, _ := newWebSocketTestHandler(t, resources)
	gatewayServer := httptest.NewServer(handler)
	defer gatewayServer.Close()

	type connection struct {
		close  io.Closer
		stream io.ReadWriter
	}
	connections := make([]connection, 0, profile.Tunnels)
	defer func() {
		for _, connection := range connections {
			_ = connection.close.Close()
		}
	}()
	latencies := make([]time.Duration, 0, profile.Tunnels*profile.MessagesPerTunnel)
	started := time.Now()
	for range profile.Tunnels {
		networkConnection, buffered, response := dialTestWebSocket(t, gatewayServer.Listener.Addr().String())
		if response.StatusCode != 101 {
			t.Fatalf("handshake status=%d", response.StatusCode)
		}
		connections = append(connections, connection{close: networkConnection, stream: buffered})
	}
	waitForPhase3C3Ownership(t, manager, tunnels, profile.Tunnels)

	payload := []byte("phase3c3-opaque-message")
	buffer := make([]byte, len(payload))
	for message := range profile.MessagesPerTunnel {
		for index := range connections {
			begin := time.Now()
			if _, err := connections[index].stream.Write(payload); err != nil {
				t.Fatal(err)
			}
			if flusher, ok := connections[index].stream.(interface{ Flush() error }); ok {
				if err := flusher.Flush(); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := io.ReadFull(connections[index].stream, buffer); err != nil {
				t.Fatal(err)
			}
			latencies = append(latencies, time.Since(begin))
		}
		if message < profile.Rotations {
			rotated := model.CloneResourceSet(resources)
			idle := time.Duration(message+2) * time.Minute
			rotated.Routes[0].WebSocket.IdleTimeout = &idle
			if err := manager.Apply(uint64(message+2), rotated); err != nil {
				t.Fatalf("Apply(%d) error=%v", message+2, err)
			}
		}
	}
	for _, connection := range connections {
		_ = connection.close.Close()
	}
	waitForPhase3C3Cleanup(t, manager, tunnels)

	runtime.GC()
	var after runtime.MemStats
	runtime.ReadMemStats(&after)
	sort.Slice(latencies, func(i, j int) bool { return latencies[i] < latencies[j] })
	p99 := latencies[(len(latencies)-1)*99/100]
	elapsed := time.Since(started)
	messages := profile.Tunnels * profile.MessagesPerTunnel
	t.Logf(
		"seed=%d os=%s arch=%s go=%s tunnels=%d messages=%d rotations=%d heap_delta=%d goroutine_delta=%d throughput=%.2f_msg/s p99=%s",
		seed, runtime.GOOS, runtime.GOARCH, runtime.Version(), profile.Tunnels, messages,
		profile.Rotations, int64(after.HeapAlloc)-int64(before.HeapAlloc), runtime.NumGoroutine()-baselineGoroutines,
		float64(messages)/elapsed.Seconds(), p99,
	)
	if full {
		runPhase3C3RelativeGates(t, upstreamServer.Listener.Addr().String(), gatewayServer.Listener.Addr().String(), profile)
	}
}

func runPhase3C3RelativeGates(t *testing.T, directAddress, gatewayAddress string, profile phase3C3Profile) {
	t.Helper()
	directHandshake := make([]phase3C3Measurement, 0, 5)
	gatewayHandshake := make([]phase3C3Measurement, 0, 5)
	directMessage := make([]phase3C3Measurement, 0, 5)
	gatewayMessage := make([]phase3C3Measurement, 0, 5)
	for round := range 5 {
		measure := func(address string) (phase3C3Measurement, phase3C3Measurement) {
			return measurePhase3C3Handshakes(t, address, profile.Tunnels),
				measurePhase3C3Messages(t, address, profile.Tunnels*profile.MessagesPerTunnel)
		}
		if round%2 == 0 {
			handshake, message := measure(directAddress)
			directHandshake, directMessage = append(directHandshake, handshake), append(directMessage, message)
			handshake, message = measure(gatewayAddress)
			gatewayHandshake, gatewayMessage = append(gatewayHandshake, handshake), append(gatewayMessage, message)
		} else {
			handshake, message := measure(gatewayAddress)
			gatewayHandshake, gatewayMessage = append(gatewayHandshake, handshake), append(gatewayMessage, message)
			handshake, message = measure(directAddress)
			directHandshake, directMessage = append(directHandshake, handshake), append(directMessage, message)
		}
	}
	directHandshakeMedian := medianPhase3C3Measurement(directHandshake)
	gatewayHandshakeMedian := medianPhase3C3Measurement(gatewayHandshake)
	directMessageMedian := medianPhase3C3Measurement(directMessage)
	gatewayMessageMedian := medianPhase3C3Measurement(gatewayMessage)
	t.Logf(
		"direct_handshake=%.2f_ops/s p99=%s gateway_handshake=%.2f_ops/s p99=%s direct_message=%.2f_ops/s p99=%s gateway_message=%.2f_ops/s p99=%s",
		directHandshakeMedian.throughput, directHandshakeMedian.p99,
		gatewayHandshakeMedian.throughput, gatewayHandshakeMedian.p99,
		directMessageMedian.throughput, directMessageMedian.p99,
		gatewayMessageMedian.throughput, gatewayMessageMedian.p99,
	)
	if gatewayHandshakeMedian.throughput < directHandshakeMedian.throughput*0.90 {
		t.Fatal("handshake throughput gate")
	}
	if gatewayHandshakeMedian.p99 > directHandshakeMedian.p99*125/100 {
		t.Fatal("handshake p99 gate")
	}
	if gatewayMessageMedian.throughput < directMessageMedian.throughput*0.95 {
		t.Fatal("message throughput gate")
	}
	if gatewayMessageMedian.p99 > directMessageMedian.p99*110/100 {
		t.Fatal("message p99 gate")
	}
}

func measurePhase3C3Handshakes(t *testing.T, address string, count int) phase3C3Measurement {
	t.Helper()
	latencies := make([]time.Duration, 0, count)
	started := time.Now()
	for range count {
		begin := time.Now()
		connection, _, response := dialTestWebSocket(t, address)
		latencies = append(latencies, time.Since(begin))
		if response.StatusCode != 101 {
			t.Fatalf("handshake status=%d", response.StatusCode)
		}
		_ = connection.Close()
	}
	return phase3C3Measured(latencies, time.Since(started))
}

func measurePhase3C3Messages(t *testing.T, address string, count int) phase3C3Measurement {
	t.Helper()
	connection, buffered, response := dialTestWebSocket(t, address)
	defer connection.Close()
	if response.StatusCode != 101 {
		t.Fatalf("handshake status=%d", response.StatusCode)
	}
	payload := []byte("phase3c3-message")
	received := make([]byte, len(payload))
	latencies := make([]time.Duration, 0, count)
	started := time.Now()
	for range count {
		begin := time.Now()
		if _, err := buffered.Write(payload); err != nil {
			t.Fatal(err)
		}
		if err := buffered.Flush(); err != nil {
			t.Fatal(err)
		}
		if _, err := io.ReadFull(buffered, received); err != nil {
			t.Fatal(err)
		}
		latencies = append(latencies, time.Since(begin))
	}
	return phase3C3Measured(latencies, time.Since(started))
}

func phase3C3Measured(latencies []time.Duration, elapsed time.Duration) phase3C3Measurement {
	sort.Slice(latencies, func(i, j int) bool { return latencies[i] < latencies[j] })
	return phase3C3Measurement{
		throughput: float64(len(latencies)) / elapsed.Seconds(),
		p99:        latencies[(len(latencies)-1)*99/100],
	}
}

func medianPhase3C3Measurement(measurements []phase3C3Measurement) phase3C3Measurement {
	throughputs := make([]float64, len(measurements))
	p99s := make([]time.Duration, len(measurements))
	for index, measurement := range measurements {
		throughputs[index], p99s[index] = measurement.throughput, measurement.p99
	}
	sort.Float64s(throughputs)
	sort.Slice(p99s, func(i, j int) bool { return p99s[i] < p99s[j] })
	middle := len(measurements) / 2
	return phase3C3Measurement{throughput: throughputs[middle], p99: p99s[middle]}
}

func waitForPhase3C3Cleanup(t *testing.T, manager interface{ UpstreamStats() upstream.RegistryStats }, tunnels *tunnel.Registry) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		stats := manager.UpstreamStats()
		if tunnels.Stats() == (tunnel.Stats{}) && stats.LiveTunnelLeases == 0 && stats.RetiredPlanSets == 0 {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	stats := manager.UpstreamStats()
	t.Fatalf("cleanup tunnels=%+v leases=%d retired=%d", tunnels.Stats(), stats.LiveTunnelLeases, stats.RetiredPlanSets)
}

func waitForPhase3C3Ownership(t *testing.T, manager interface{ UpstreamStats() upstream.RegistryStats }, tunnels *tunnel.Registry, want int) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if tunnels.Stats().Active == uint64(want) && manager.UpstreamStats().LiveTunnelLeases == want {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("ownership tunnels=%+v leases=%d, want %d", tunnels.Stats(), manager.UpstreamStats().LiveTunnelLeases, want)
}

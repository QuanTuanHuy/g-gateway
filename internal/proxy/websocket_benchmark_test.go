package proxy

import (
	"bufio"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"

	websocketpkg "github.com/QuanTuanHuy/g-gateway/internal/websocket"
)

func BenchmarkPhase3C3HandshakeValidation(b *testing.B) {
	request := httptest.NewRequest(http.MethodGet, "http://gateway.test/events", nil)
	request.Header.Set("Connection", "Upgrade")
	request.Header.Set("Upgrade", "websocket")
	request.Header.Set("Sec-WebSocket-Version", "13")
	request.Header.Set("Sec-WebSocket-Key", proxyWebSocketKey)
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		if _, err := websocketpkg.ValidateRequest(request); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkPhase3C3Handshake(b *testing.B) {
	upstreamServer := newUpgradeTestServer(b, "", nil)
	handler, _, _, _ := newWebSocketTestHandler(b, webSocketResources(upstreamServer.URL, true))
	gatewayServer := httptest.NewServer(handler)
	b.Cleanup(gatewayServer.Close)
	for _, benchmark := range []struct {
		name    string
		address string
	}{
		{name: "direct", address: upstreamServer.Listener.Addr().String()},
		{name: "gateway", address: gatewayServer.Listener.Addr().String()},
	} {
		b.Run(benchmark.name, func(b *testing.B) {
			b.ReportAllocs()
			for range b.N {
				connection, _, response := dialTestWebSocket(b, benchmark.address)
				if response.StatusCode != http.StatusSwitchingProtocols {
					b.Fatalf("status=%d", response.StatusCode)
				}
				_ = connection.Close()
			}
		})
	}
}

func BenchmarkPhase3C3Message(b *testing.B) {
	upstreamServer := newUpgradeTestServer(b, "", nil)
	handler, _, _, _ := newWebSocketTestHandler(b, webSocketResources(upstreamServer.URL, true))
	gatewayServer := httptest.NewServer(handler)
	b.Cleanup(gatewayServer.Close)
	for _, benchmark := range []struct {
		name    string
		address string
	}{
		{name: "direct", address: upstreamServer.Listener.Addr().String()},
		{name: "gateway", address: gatewayServer.Listener.Addr().String()},
	} {
		b.Run(benchmark.name, func(b *testing.B) {
			connection, buffered, response := dialTestWebSocket(b, benchmark.address)
			b.Cleanup(func() { _ = connection.Close() })
			if response.StatusCode != http.StatusSwitchingProtocols {
				b.Fatalf("status=%d", response.StatusCode)
			}
			benchmarkPhase3C3Messages(b, connection, buffered)
		})
	}
}

func benchmarkPhase3C3Messages(b *testing.B, connection net.Conn, buffered *bufio.ReadWriter) {
	payload := []byte("phase3c3-message")
	received := make([]byte, len(payload))
	b.ReportAllocs()
	b.SetBytes(int64(len(payload) * 2))
	b.ResetTimer()
	for range b.N {
		if _, err := buffered.Write(payload); err != nil {
			b.Fatal(err)
		}
		if err := buffered.Flush(); err != nil {
			b.Fatal(err)
		}
		if _, err := io.ReadFull(buffered, received); err != nil {
			b.Fatal(err)
		}
	}
}

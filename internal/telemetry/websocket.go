package telemetry

import "github.com/QuanTuanHuy/g-gateway/internal/tunnel"

// ObserveWebSocketHandshake increments one pre-bound bounded result series.
func (t *Telemetry) ObserveWebSocketHandshake(result string) {
	index, ok := webSocketHandshakeIndex(result)
	if !ok {
		return
	}
	t.webSocketHandshakes[index].Inc()
}

// TunnelOpened increments the active WebSocket tunnel gauge.
func (t *Telemetry) TunnelOpened() {
	t.webSocketActive.Inc()
}

// TunnelBytes adds opaque transferred bytes to one bounded direction series.
func (t *Telemetry) TunnelBytes(direction tunnel.Direction, count uint64) {
	index, ok := webSocketDirectionIndex(direction)
	if !ok {
		return
	}
	t.webSocketBytes[index].Add(float64(count))
}

// TunnelClosed records one valid bounded terminal result and decrements the
// active tunnel gauge.
func (t *Telemetry) TunnelClosed(result tunnel.Result) {
	index, ok := webSocketCloseReasonIndex(result.Reason)
	if !ok {
		return
	}
	t.webSocketActive.Dec()
	t.webSocketClosed[index].Inc()
	t.webSocketDuration.Observe(result.Duration.Seconds())
}

func webSocketHandshakeIndex(result string) (int, bool) {
	switch result {
	case "success":
		return 0, true
	case "invalid_request":
		return 1, true
	case "upstream_rejected":
		return 2, true
	case "upstream_failure":
		return 3, true
	case "plugin_failure":
		return 4, true
	case "draining":
		return 5, true
	default:
		return 0, false
	}
}

func webSocketCloseReasonIndex(reason tunnel.CloseReason) (int, bool) {
	switch reason {
	case tunnel.ReasonClientEOF:
		return 0, true
	case tunnel.ReasonUpstreamEOF:
		return 1, true
	case tunnel.ReasonIdleTimeout:
		return 2, true
	case tunnel.ReasonShutdown:
		return 3, true
	case tunnel.ReasonIOError:
		return 4, true
	default:
		return 0, false
	}
}

func webSocketDirectionIndex(direction tunnel.Direction) (int, bool) {
	switch direction {
	case tunnel.DirectionDownstreamToUpstream:
		return 0, true
	case tunnel.DirectionUpstreamToDownstream:
		return 1, true
	default:
		return 0, false
	}
}

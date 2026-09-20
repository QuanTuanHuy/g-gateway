package accesslog

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"math"
	"time"
	"unicode/utf8"
)

const (
	// Schema identifies the Phase 3D1 access-event contract.
	Schema = "gateway.access/v1"
	// MaxStringBytes bounds every encoded access-event string.
	MaxStringBytes = 256
	// MaxEncodedBytes bounds one JSON event including its newline.
	MaxEncodedBytes = 4096
)

// Event is one immutable access-event candidate. Normalize must be called
// before an Event is submitted for output.
type Event struct {
	Schema            string
	Timestamp         string
	RequestID         string
	Revision          uint64
	Method            string
	Protocol          string
	RouteID           string
	ServiceID         string
	UpstreamID        string
	EndpointID        string
	ResponseSource    string
	Status            int
	RequestBodyBytes  uint64
	ResponseBodyBytes uint64
	Attempts          int
	UpstreamOutcome   string
	RetrySuppressed   string
	ErrorCode         string
	Termination       string
	DurationUS        uint64
	UpstreamWaitUS    uint64
	ResponseStreamUS  uint64
}

// Normalize validates closed values and returns a bounded event containing no
// caller-owned mutable data.
func Normalize(event Event) (Event, error) {
	timestamp, err := time.Parse(time.RFC3339Nano, event.Timestamp)
	if err != nil {
		return Event{}, fmt.Errorf("timestamp: %w", err)
	}
	if !validResponseSource(event.ResponseSource) {
		return Event{}, errors.New("response_source: invalid value")
	}
	if !validTermination(event.Termination) {
		return Event{}, errors.New("termination: invalid value")
	}
	if !validUpstreamOutcome(event.UpstreamOutcome) {
		return Event{}, errors.New("upstream_outcome: invalid value")
	}
	if !validRetrySuppressed(event.RetrySuppressed) {
		return Event{}, errors.New("retry_suppressed: invalid value")
	}
	if event.Status < 0 {
		return Event{}, errors.New("status: must not be negative")
	}
	if event.Attempts < 0 {
		return Event{}, errors.New("attempts: must not be negative")
	}

	event.Schema = Schema
	event.Timestamp = timestamp.UTC().Format(time.RFC3339Nano)
	event.RequestID = boundedString(event.RequestID)
	event.Method = boundedString(event.Method)
	event.Protocol = boundedString(event.Protocol)
	event.RouteID = boundedString(event.RouteID)
	event.ServiceID = boundedString(event.ServiceID)
	event.UpstreamID = boundedString(event.UpstreamID)
	event.EndpointID = EndpointID(event.EndpointID)
	event.ErrorCode = boundedString(event.ErrorCode)
	if encodedEventSize(event) > MaxEncodedBytes {
		return Event{}, errors.New("event: encoded size exceeds maximum")
	}
	return event, nil
}

// EndpointID returns a bounded hash of a canonical endpoint identity. An empty
// identity remains empty so requests without a selection are distinguishable.
func EndpointID(identity string) string {
	if identity == "" {
		return ""
	}
	return hashString(identity)
}

func boundedString(value string) string {
	if utf8.ValidString(value) && len(value) <= MaxStringBytes {
		return value
	}
	return hashString(value)
}

func hashString(value string) string {
	sum := sha256.Sum256([]byte(value))
	return "sha256:" + hex.EncodeToString(sum[:])
}

func validResponseSource(value string) bool {
	switch value {
	case "gateway", "plugin", "upstream", "websocket":
		return true
	default:
		return false
	}
}

func validTermination(value string) bool {
	switch value {
	case "completed", "client_canceled", "downstream_write_error", "panic", "websocket_upgraded":
		return true
	default:
		return false
	}
}

func validUpstreamOutcome(value string) bool {
	switch value {
	case "", "success", "timeout", "transport_failure", "retryable_status":
		return true
	default:
		return false
	}
}

func validRetrySuppressed(value string) bool {
	switch value {
	case "", "body_not_replayable", "request_not_replayable", "attempt_limit", "budget", "no_untried_endpoint":
		return true
	default:
		return false
	}
}

func saturatingAdd(left, right uint64) uint64 {
	if math.MaxUint64-left < right {
		return math.MaxUint64
	}
	return left + right
}

func durationMicroseconds(duration time.Duration) uint64 {
	if duration <= 0 {
		return 0
	}
	return uint64(duration / time.Microsecond)
}

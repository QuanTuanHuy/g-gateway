package accesslog

import (
	"math"
	"strings"
	"testing"
	"time"
)

func TestEndpointIDIsAlwaysHashed(t *testing.T) {
	const identity = "users\x00http://10.0.0.1:8080"
	const want = "sha256:25ce3a22d877b129609e5e0ae332b6f29bbd665e60bd14536f3976256852f264"
	if got := EndpointID(identity); got != want {
		t.Fatalf("endpoint id = %q, want %q", got, want)
	}
	if got := EndpointID(""); got != "" {
		t.Fatalf("empty endpoint id = %q", got)
	}
}

func TestBoundedStringPreservesOnlyValidValuesWithinLimit(t *testing.T) {
	within := strings.Repeat("a", MaxStringBytes)
	if got := boundedString(within); got != within {
		t.Fatalf("256-byte value changed to %q", got)
	}
	for name, value := range map[string]string{
		"over limit":   strings.Repeat("a", MaxStringBytes+1),
		"invalid UTF8": string([]byte{0xff, 0xfe}),
	} {
		t.Run(name, func(t *testing.T) {
			got := boundedString(value)
			if !strings.HasPrefix(got, "sha256:") || len(got) != len("sha256:")+64 {
				t.Fatalf("bounded value = %q", got)
			}
		})
	}
}

func TestEventNormalizeRejectsUnknownClosedValues(t *testing.T) {
	tests := map[string]func(*Event){
		"response source":  func(event *Event) { event.ResponseSource = "raw-input" },
		"termination":      func(event *Event) { event.Termination = "raw-input" },
		"upstream outcome": func(event *Event) { event.UpstreamOutcome = "raw-input" },
		"retry suppressed": func(event *Event) { event.RetrySuppressed = "raw-input" },
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			event := validEvent()
			mutate(&event)
			if _, err := Normalize(event); err == nil {
				t.Fatal("Normalize succeeded, want error")
			}
		})
	}
}

func TestEventNormalizeRejectsNegativeStatusAndAttempts(t *testing.T) {
	for name, mutate := range map[string]func(*Event){
		"status":   func(event *Event) { event.Status = -1 },
		"attempts": func(event *Event) { event.Attempts = -1 },
	} {
		t.Run(name, func(t *testing.T) {
			event := validEvent()
			mutate(&event)
			if _, err := Normalize(event); err == nil {
				t.Fatal("Normalize succeeded, want error")
			}
		})
	}
}

func TestBoundedCountersSaturate(t *testing.T) {
	if got := saturatingAdd(math.MaxUint64-1, 10); got != math.MaxUint64 {
		t.Fatalf("saturating add = %d", got)
	}
	if got := durationMicroseconds(-time.Second); got != 0 {
		t.Fatalf("negative duration = %d", got)
	}
	if got := durationMicroseconds(1500 * time.Nanosecond); got != 1 {
		t.Fatalf("duration microseconds = %d", got)
	}
}

func validEvent() Event {
	return Event{
		Timestamp:       "2026-09-20T14:15:16.123456789+07:00",
		RequestID:       "request-1",
		Method:          "GET",
		Protocol:        "HTTP/1.1",
		ResponseSource:  "upstream",
		Status:          200,
		Attempts:        1,
		UpstreamOutcome: "success",
		Termination:     "completed",
	}
}

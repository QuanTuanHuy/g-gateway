package accesslog

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestEncodeEventGolden(t *testing.T) {
	event := validEvent()
	event.Revision = 42
	event.RouteID = "users"
	event.ServiceID = "users-service"
	event.UpstreamID = "users-upstream"
	event.EndpointID = "users\x00http://10.0.0.1:8080"
	event.RequestBodyBytes = 11
	event.ResponseBodyBytes = 29
	event.DurationUS = 101
	event.UpstreamWaitUS = 37
	event.ResponseStreamUS = 53

	normalized, err := Normalize(event)
	if err != nil {
		t.Fatal(err)
	}
	got, err := encode(normalized)
	if err != nil {
		t.Fatal(err)
	}
	want := "{\"schema\":\"gateway.access/v1\",\"timestamp\":\"2026-09-20T07:15:16.123456789Z\",\"request_id\":\"request-1\",\"revision\":42,\"method\":\"GET\",\"protocol\":\"HTTP/1.1\",\"route_id\":\"users\",\"service_id\":\"users-service\",\"upstream_id\":\"users-upstream\",\"endpoint_id\":\"sha256:25ce3a22d877b129609e5e0ae332b6f29bbd665e60bd14536f3976256852f264\",\"response_source\":\"upstream\",\"status\":200,\"request_body_bytes\":11,\"response_body_bytes\":29,\"attempts\":1,\"upstream_outcome\":\"success\",\"retry_suppressed\":\"\",\"error_code\":\"\",\"termination\":\"completed\",\"duration_us\":101,\"upstream_wait_us\":37,\"response_stream_us\":53}\n"
	if string(got) != want {
		t.Fatalf("encoded event:\n got: %s\nwant: %s", got, want)
	}
}

func TestEncodeRejectsRecordOverMaximum(t *testing.T) {
	wire := eventWire{Schema: strings.Repeat("x", MaxEncodedBytes)}
	if _, err := encodeWire(wire); err == nil {
		t.Fatal("encodeWire succeeded, want size error")
	}
}

func TestEncodeAcceptsRecordExactlyAtMaximum(t *testing.T) {
	wire := eventWire{}
	base, err := json.Marshal(wire)
	if err != nil {
		t.Fatal(err)
	}
	wire.Schema = strings.Repeat("x", MaxEncodedBytes-(len(base)+1))
	got, err := encodeWire(wire)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != MaxEncodedBytes {
		t.Fatalf("encoded bytes = %d, want %d", len(got), MaxEncodedBytes)
	}
}

func TestEncodedEventCannotContainForbiddenInputs(t *testing.T) {
	markers := []string{
		"path-secret-7bd9", "query-secret-7bd9", "host-secret-7bd9",
		"header-secret-7bd9", "cookie-secret-7bd9", "authorization-secret-7bd9",
		"request-body-secret-7bd9", "response-body-secret-7bd9", "client-ip-secret-7bd9",
		"upstream-url-secret-7bd9", "certificate-secret-7bd9", "raw-error-secret-7bd9",
		"stack-secret-7bd9", "plugin-config-secret-7bd9",
	}
	event, err := Normalize(validEvent())
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := encode(event)
	if err != nil {
		t.Fatal(err)
	}
	for _, marker := range markers {
		if strings.Contains(string(encoded), marker) {
			t.Fatalf("encoded event leaked %q", marker)
		}
	}
}

func TestEncodedEventSizeMatchesCanonicalJSON(t *testing.T) {
	event := validEvent()
	event.RequestID = "quoted\" control\x00 html<&> line\u2028"
	event.Revision = ^uint64(0)
	event.RequestBodyBytes = ^uint64(0)
	event.ResponseBodyBytes = ^uint64(0)
	event.DurationUS = ^uint64(0)
	event.UpstreamWaitUS = ^uint64(0)
	event.ResponseStreamUS = ^uint64(0)
	normalized, err := Normalize(event)
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := encode(normalized)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := encodedEventSize(normalized), len(encoded); got != want {
		t.Fatalf("encoded size = %d, want %d", got, want)
	}
}

package plugin

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/QuanTuanHuy/g-gateway/internal/model"
	"github.com/QuanTuanHuy/g-gateway/internal/requestctx"
)

func TestRequestIDReplacesInvalidInputAndReturnsUUID(t *testing.T) {
	definition := requestIDDefinition(bytes.NewReader([]byte{
		0x00, 0x11, 0x22, 0x33, 0x44, 0x55, 0x66, 0x77,
		0x88, 0x99, 0xaa, 0xbb, 0xcc, 0xdd, 0xee, 0xff,
	}))
	compiled, err := definition.Compile(json.RawMessage(`{"header_name":"X-Request-ID","max_input_length":128}`))
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodGet, "http://gateway/", nil)
	request.Header.Set("X-Request-ID", "contains newline\n")
	state := &requestctx.Context{}
	if result := compiled.Request.OnRequest(state, request); result.Err != nil {
		t.Fatal(result.Err)
	}
	const want = "00112233-4455-4677-8899-aabbccddeeff"
	if state.RequestID != want || request.Header.Get("X-Request-ID") != want {
		t.Fatalf("state/header = %q/%q", state.RequestID, request.Header.Get("X-Request-ID"))
	}
	response := &http.Response{Header: make(http.Header)}
	response.Header.Set("X-Request-ID", "rewritten")
	if err := compiled.Response.OnResponse(state, response); err != nil {
		t.Fatal(err)
	}
	if response.Header.Get("X-Request-ID") != want {
		t.Fatalf("response header = %q", response.Header.Get("X-Request-ID"))
	}
}

func TestRequestIDPreservesValidInboundValues(t *testing.T) {
	allowed := "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789._:-"
	for _, value := range []string{"a", allowed, strings.Repeat("x", 128)} {
		t.Run(value[:1], func(t *testing.T) {
			compiled := mustCompileRequestID(t, errorReader{err: errors.New("random must not be read")}, nil)
			request := httptest.NewRequest(http.MethodGet, "http://gateway/", nil)
			request.Header.Set("X-Request-ID", value)
			state := &requestctx.Context{}

			result := compiled.Request.OnRequest(state, request)
			if result.Err != nil {
				t.Fatal(result.Err)
			}
			if state.RequestID != value || request.Header.Get("X-Request-ID") != value {
				t.Fatalf("state/header = %q/%q", state.RequestID, request.Header.Get("X-Request-ID"))
			}
		})
	}
}

func TestRequestIDGeneratesForMissingMultipleAndTooLongInput(t *testing.T) {
	tests := []struct {
		name   string
		values []string
	}{
		{name: "missing"},
		{name: "multiple", values: []string{"one", "two"}},
		{name: "too long", values: []string{strings.Repeat("x", 129)}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			compiled := mustCompileRequestID(t, bytes.NewReader(make([]byte, 16)), nil)
			request := httptest.NewRequest(http.MethodGet, "http://gateway/", nil)
			for _, value := range tt.values {
				request.Header.Add("X-Request-ID", value)
			}
			state := &requestctx.Context{}

			result := compiled.Request.OnRequest(state, request)
			if result.Err != nil {
				t.Fatal(result.Err)
			}
			const want = "00000000-0000-4000-8000-000000000000"
			if state.RequestID != want || request.Header.Get("X-Request-ID") != want {
				t.Fatalf("state/header = %q/%q", state.RequestID, request.Header.Get("X-Request-ID"))
			}
			if len(request.Header.Values("X-Request-ID")) != 1 {
				t.Fatalf("header values = %v", request.Header.Values("X-Request-ID"))
			}
		})
	}
}

func TestRequestIDCustomHeaderAndLength(t *testing.T) {
	compiled := mustCompileRequestID(
		t,
		errorReader{err: errors.New("random must not be read")},
		json.RawMessage(`{"header_name":"X-Correlation-ID","max_input_length":1}`),
	)
	request := httptest.NewRequest(http.MethodGet, "http://gateway/", nil)
	request.Header.Set("X-Correlation-ID", "z")
	state := &requestctx.Context{}

	if result := compiled.Request.OnRequest(state, request); result.Err != nil {
		t.Fatal(result.Err)
	}
	if state.RequestID != "z" || request.Header.Get("X-Correlation-ID") != "z" {
		t.Fatalf("state/header = %q/%q", state.RequestID, request.Header.Get("X-Correlation-ID"))
	}
	response := &http.Response{}
	if err := compiled.Response.OnResponse(state, response); err != nil {
		t.Fatal(err)
	}
	if response.Header.Get("X-Correlation-ID") != "z" {
		t.Fatalf("response header = %q", response.Header.Get("X-Correlation-ID"))
	}
}

func TestRequestIDRejectsInvalidConfiguration(t *testing.T) {
	definition := requestIDDefinition(bytes.NewReader(make([]byte, 16)))
	tests := []string{
		`{"header_name":"Bad Header"}`,
		`{"header_name":"Connection"}`,
		`{"header_name":"Content-Length"}`,
		`{"header_name":":authority"}`,
		`{"max_input_length":0}`,
		`{"max_input_length":1025}`,
		`{"unknown":true}`,
		`{"header_name":`,
		`{} {}`,
	}
	for _, raw := range tests {
		t.Run(raw, func(t *testing.T) {
			if _, err := definition.Compile(json.RawMessage(raw)); err == nil {
				t.Fatal("Compile() unexpectedly succeeded")
			}
		})
	}
}

func TestRequestIDReturnsSecureRandomFailure(t *testing.T) {
	compiled := mustCompileRequestID(t, errorReader{err: errors.New("entropy unavailable")}, nil)
	request := httptest.NewRequest(http.MethodGet, "http://gateway/", nil)
	result := compiled.Request.OnRequest(&requestctx.Context{}, request)
	if result.Err == nil || !strings.Contains(result.Err.Error(), "entropy unavailable") {
		t.Fatalf("OnRequest() error = %v", result.Err)
	}
}

func TestRequestIDBuiltinRegistry(t *testing.T) {
	registry, err := NewBuiltinRegistry()
	if err != nil {
		t.Fatal(err)
	}
	chain, err := registry.CompileChain(nil, []model.PluginAttachment{{
		Name:      "request-id",
		Enabled:   true,
		RawConfig: json.RawMessage(`{}`),
	}})
	if err != nil {
		t.Fatal(err)
	}
	if names := chain.Names(); len(names) != 1 || names[0] != "request-id" {
		t.Fatalf("Names() = %v", names)
	}
}

func mustCompileRequestID(t *testing.T, random io.Reader, raw json.RawMessage) CompiledPlugin {
	t.Helper()
	compiled, err := requestIDDefinition(random).Compile(raw)
	if err != nil {
		t.Fatal(err)
	}
	return compiled
}

type errorReader struct {
	err error
}

func (r errorReader) Read([]byte) (int, error) {
	return 0, r.err
}

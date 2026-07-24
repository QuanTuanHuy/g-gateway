package plugin

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/QuanTuanHuy/g-gateway/internal/model"
	"github.com/QuanTuanHuy/g-gateway/internal/requestctx"
)

func TestHeaderRewriteMutatesRequestAndResponse(t *testing.T) {
	registry, err := NewBuiltinRegistry()
	if err != nil {
		t.Fatal(err)
	}
	chain, err := registry.CompileChain(nil, []model.PluginAttachment{{
		Name:    "header-rewrite",
		Enabled: true,
		RawConfig: json.RawMessage(`{
			"request":{
				"remove":["X-Remove"],
				"set":{"X-Set":"request","X-Array":["one","two"]},
				"add":{"X-Add":["a","b"]}
			},
			"response":{
				"remove":["X-Hide"],
				"set":{"X-Set":"response"},
				"add":{"Set-Cookie":["a=1","b=2"]}
			}
		}`),
	}})
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodGet, "http://gateway/", nil)
	request.Header.Set("X-Remove", "old")
	request.Header.Set("X-Set", "old")
	request.Header.Set("X-Add", "existing")
	state := &requestctx.Context{}
	if result := chain.RunRequest(state, request); result.Err != nil {
		t.Fatal(result.Err)
	}
	if request.Header.Get("X-Remove") != "" ||
		request.Header.Get("X-Set") != "request" ||
		!reflect.DeepEqual(request.Header.Values("X-Array"), []string{"one", "two"}) ||
		!reflect.DeepEqual(request.Header.Values("X-Add"), []string{"existing", "a", "b"}) {
		t.Fatalf("request headers = %#v", request.Header)
	}

	response := &http.Response{Header: http.Header{"X-Hide": {"secret"}}}
	if err := chain.RunResponse(state, response); err != nil {
		t.Fatal(err)
	}
	if response.Header.Get("X-Hide") != "" ||
		response.Header.Get("X-Set") != "response" ||
		!reflect.DeepEqual(response.Header.Values("Set-Cookie"), []string{"a=1", "b=2"}) {
		t.Fatalf("response headers = %#v", response.Header)
	}
}

func TestHeaderRewriteRejectsInvalidConfiguration(t *testing.T) {
	definition := headerRewriteDefinition()
	tests := []struct {
		name string
		raw  string
	}{
		{name: "unknown root field", raw: `{"unknown":true}`},
		{name: "unknown direction field", raw: `{"request":{"unknown":[]}}`},
		{name: "invalid header name", raw: `{"request":{"set":{"Bad Header":"x"}}}`},
		{name: "invalid header value newline", raw: "{\"request\":{\"set\":{\"X-Test\":\"bad\\nvalue\"}}}"},
		{name: "empty value array", raw: `{"request":{"set":{"X-Test":[]}}}`},
		{name: "non string value", raw: `{"request":{"set":{"X-Test":1}}}`},
		{name: "collision normalized name", raw: `{"request":{"remove":["x-test"],"set":{"X-Test":"value"}}}`},
		{name: "duplicate remove normalized name", raw: `{"request":{"remove":["x-test","X-Test"]}}`},
		{name: "pseudo header", raw: `{"request":{"remove":[":authority"]}}`},
	}
	for _, protected := range []string{
		"Host",
		"Content-Length",
		"Connection",
		"Keep-Alive",
		"Proxy-Connection",
		"TE",
		"Trailer",
		"Transfer-Encoding",
		"Upgrade",
	} {
		tests = append(tests, struct {
			name string
			raw  string
		}{
			name: "protected " + protected,
			raw:  `{"response":{"set":{"` + protected + `":"value"}}}`,
		})
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := definition.Compile(json.RawMessage(tt.raw)); err == nil {
				t.Fatalf("Compile(%s) unexpectedly succeeded", tt.raw)
			}
		})
	}
}

func TestHeaderRewriteBuiltinOrderingRestoresRequestIDOnResponse(t *testing.T) {
	registry, err := NewBuiltinRegistry()
	if err != nil {
		t.Fatal(err)
	}
	chain, err := registry.CompileChain(nil, []model.PluginAttachment{
		{Name: "header-rewrite", Enabled: true, RawConfig: json.RawMessage(`{
			"request":{"set":{"X-Request-ID":"request-rewritten"}},
			"response":{"set":{"X-Request-ID":"response-rewritten"}}
		}`)},
		{Name: "request-id", Enabled: true, RawConfig: json.RawMessage(`{}`)},
	})
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodGet, "http://gateway/", nil)
	request.Header.Set("X-Request-ID", "incoming-safe")
	state := &requestctx.Context{}

	if result := chain.RunRequest(state, request); result.Err != nil {
		t.Fatal(result.Err)
	}
	if state.RequestID != "incoming-safe" || request.Header.Get("X-Request-ID") != "request-rewritten" {
		t.Fatalf("state/request ID = %q/%q", state.RequestID, request.Header.Get("X-Request-ID"))
	}
	response := &http.Response{Header: make(http.Header)}
	if err := chain.RunResponse(state, response); err != nil {
		t.Fatal(err)
	}
	if got := response.Header.Get("X-Request-ID"); got != "incoming-safe" {
		t.Fatalf("response request ID = %q", got)
	}
}

func TestHeaderRewriteCompiledValuesAreImmutable(t *testing.T) {
	compiled, err := headerRewriteDefinition().Compile(json.RawMessage(`{
		"request":{"set":{"X-Test":["one","two"]}}
	}`))
	if err != nil {
		t.Fatal(err)
	}
	first := httptest.NewRequest(http.MethodGet, "http://gateway/", nil)
	if result := compiled.Request.OnRequest(&requestctx.Context{}, first); result.Err != nil {
		t.Fatal(result.Err)
	}
	first.Header["X-Test"][0] = "mutated"

	second := httptest.NewRequest(http.MethodGet, "http://gateway/", nil)
	if result := compiled.Request.OnRequest(&requestctx.Context{}, second); result.Err != nil {
		t.Fatal(result.Err)
	}
	if got := strings.Join(second.Header.Values("X-Test"), ","); got != "one,two" {
		t.Fatalf("second values = %q", got)
	}
}

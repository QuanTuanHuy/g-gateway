package plugin

import (
	"encoding/json"
	"errors"
	"net/http"
	"reflect"
	"strings"
	"testing"

	"github.com/QuanTuanHuy/g-gateway/internal/model"
	"github.com/QuanTuanHuy/g-gateway/internal/requestctx"
)

func TestCompileChainInheritsReplacesAndDisables(t *testing.T) {
	registry := mustRegistry(t,
		testDefinition("inherited", 10, 10),
		testDefinition("replaced", 20, 20),
		testDefinition("disabled", 30, 30),
	)
	service := []model.PluginAttachment{
		{Name: "inherited", Enabled: true, RawConfig: json.RawMessage(`{"value":"service-inherited"}`)},
		{Name: "replaced", Enabled: true, RawConfig: json.RawMessage(`{"value":"service-value"}`)},
		{Name: "disabled", Enabled: true, RawConfig: json.RawMessage(`{"value":"service-disabled"}`)},
	}
	route := []model.PluginAttachment{
		{Name: "replaced", Enabled: true, RawConfig: json.RawMessage(`{"value":"route-value"}`)},
		{Name: "disabled", Enabled: false},
	}

	chain, err := registry.CompileChain(service, route)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := chain.Names(), []string{"inherited", "replaced"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("Names() = %v, want %v", got, want)
	}
	state := &requestctx.Context{Scratch: []any{[]string{}}}
	if result := chain.RunRequest(state, &http.Request{}); result.Err != nil {
		t.Fatal(result.Err)
	}
	if got, want := state.Scratch[0], []string{
		"request:inherited:service-inherited",
		"request:replaced:route-value",
	}; !reflect.DeepEqual(got, want) {
		t.Fatalf("request log = %#v, want %#v", got, want)
	}
}

func TestCompileChainTotalsContiguousScratchSlots(t *testing.T) {
	definition := func(name string, order, slots int) Definition {
		return Definition{
			Name:          name,
			Version:       "v1",
			RequestOrder:  order,
			ResponseOrder: order,
			Compile: func(json.RawMessage) (CompiledPlugin, error) {
				return CompiledPlugin{ScratchSlots: slots}, nil
			},
		}
	}
	registry := mustRegistry(t, definition("first", 1, 2), definition("second", 2, 3))
	chain, err := registry.CompileChain(nil, []model.PluginAttachment{
		{Name: "second", Enabled: true},
		{Name: "first", Enabled: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	if chain.ScratchSlots() != 5 {
		t.Fatalf("ScratchSlots() = %d, want 5", chain.ScratchSlots())
	}
}

func TestPluginShortCircuitStopsLaterRequestsAndRunsResponses(t *testing.T) {
	first := testDefinition("first", 10, 10)
	stop := testDefinition("stop", 20, 20)
	later := testDefinition("later", 30, 30)
	stop.Compile = func(json.RawMessage) (CompiledPlugin, error) {
		hook := &recordingHook{
			name: "stop",
			request: RequestResult{
				Action: ShortCircuit,
				Response: &ShortCircuitResponse{
					Status:  http.StatusForbidden,
					Headers: http.Header{"X-Reason": []string{"policy"}},
					Body:    []byte("denied"),
					Code:    "DENIED",
				},
			},
		}
		return CompiledPlugin{Request: hook, Response: hook}, nil
	}
	registry := mustRegistry(t, first, stop, later)
	chain, err := registry.CompileChain(nil, []model.PluginAttachment{
		{Name: "later", Enabled: true},
		{Name: "stop", Enabled: true},
		{Name: "first", Enabled: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	state := &requestctx.Context{Scratch: []any{[]string{}}}
	result := chain.RunRequest(state, &http.Request{})
	if result.Err != nil || result.Action != ShortCircuit || result.Response.Status != http.StatusForbidden {
		t.Fatalf("RunRequest() = %+v", result)
	}
	if got, want := state.Scratch[0], []string{"request:first:", "request:stop:"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("request log = %#v, want %#v", got, want)
	}

	result.Response.Headers.Set("X-Reason", "mutated")
	result.Response.Body[0] = 'X'
	freshState := &requestctx.Context{Scratch: []any{[]string{}}}
	freshResult := chain.RunRequest(freshState, &http.Request{})
	if freshResult.Response.Headers.Get("X-Reason") != "policy" || string(freshResult.Response.Body) != "denied" {
		t.Fatalf("short-circuit response aliases hook-owned data: %+v", freshResult.Response)
	}
	if err := chain.RunResponse(state, &http.Response{}); err != nil {
		t.Fatal(err)
	}
	if got, want := state.Scratch[0], []string{
		"request:first:",
		"request:stop:",
		"response:first:",
		"response:stop:",
		"response:later:",
	}; !reflect.DeepEqual(got, want) {
		t.Fatalf("full log = %#v, want %#v", got, want)
	}
}

func TestPluginRuntimeErrorsIdentifyNameAndPhase(t *testing.T) {
	requestDefinition := testDefinition("request-broken", 1, 1)
	requestDefinition.Compile = func(json.RawMessage) (CompiledPlugin, error) {
		return CompiledPlugin{Request: &recordingHook{name: "request-broken", request: RequestResult{Err: errors.New("request failed")}}}, nil
	}
	responseDefinition := testDefinition("response-broken", 2, 2)
	responseDefinition.Compile = func(json.RawMessage) (CompiledPlugin, error) {
		return CompiledPlugin{Response: &recordingHook{name: "response-broken", response: errors.New("response failed")}}, nil
	}
	registry := mustRegistry(t, requestDefinition, responseDefinition)

	requestChain, err := registry.CompileChain(nil, []model.PluginAttachment{{Name: "request-broken", Enabled: true}})
	if err != nil {
		t.Fatal(err)
	}
	state := &requestctx.Context{Scratch: []any{[]string{}}}
	result := requestChain.RunRequest(state, &http.Request{})
	if result.Err == nil || !strings.Contains(result.Err.Error(), "request-broken") || !strings.Contains(result.Err.Error(), "request") {
		t.Fatalf("request error = %v", result.Err)
	}

	responseChain, err := registry.CompileChain(nil, []model.PluginAttachment{{Name: "response-broken", Enabled: true}})
	if err != nil {
		t.Fatal(err)
	}
	if err := responseChain.RunResponse(state, &http.Response{}); err == nil ||
		!strings.Contains(err.Error(), "response-broken") ||
		!strings.Contains(err.Error(), "response") {
		t.Fatalf("response error = %v", err)
	}
}

func TestPluginShortCircuitValidation(t *testing.T) {
	registry := mustRegistry(t, Definition{
		Name:          "large",
		Version:       "v1",
		RequestOrder:  1,
		ResponseOrder: 1,
		Compile: func(json.RawMessage) (CompiledPlugin, error) {
			return CompiledPlugin{Request: requestHookFunc(func(*requestctx.Context, *http.Request) RequestResult {
				return RequestResult{
					Action: ShortCircuit,
					Response: &ShortCircuitResponse{
						Status: http.StatusOK,
						Body:   make([]byte, 64*1024+1),
					},
				}
			})}, nil
		},
	})
	chain, err := registry.CompileChain(nil, []model.PluginAttachment{{Name: "large", Enabled: true}})
	if err != nil {
		t.Fatal(err)
	}
	if result := chain.RunRequest(&requestctx.Context{}, &http.Request{}); result.Err == nil {
		t.Fatal("RunRequest() accepted oversized short-circuit body")
	}
}

type requestHookFunc func(*requestctx.Context, *http.Request) RequestResult

func (f requestHookFunc) OnRequest(state *requestctx.Context, request *http.Request) RequestResult {
	return f(state, request)
}

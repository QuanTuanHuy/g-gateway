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

func TestPluginRegistryRejectsInvalidDefinitions(t *testing.T) {
	compiler := func(json.RawMessage) (CompiledPlugin, error) { return CompiledPlugin{}, nil }
	tests := []struct {
		name string
		defs []Definition
	}{
		{name: "empty name", defs: []Definition{{Version: "v1", Compile: compiler}}},
		{name: "empty version", defs: []Definition{{Name: "one", Compile: compiler}}},
		{name: "nil compiler", defs: []Definition{{Name: "one", Version: "v1"}}},
		{
			name: "duplicate name",
			defs: []Definition{
				{Name: "one", Version: "v1", RequestOrder: 1, ResponseOrder: 1, Compile: compiler},
				{Name: "one", Version: "v2", RequestOrder: 2, ResponseOrder: 2, Compile: compiler},
			},
		},
		{
			name: "duplicate request order",
			defs: []Definition{
				{Name: "one", Version: "v1", RequestOrder: 1, ResponseOrder: 1, Compile: compiler},
				{Name: "two", Version: "v1", RequestOrder: 1, ResponseOrder: 2, Compile: compiler},
			},
		},
		{
			name: "duplicate response order",
			defs: []Definition{
				{Name: "one", Version: "v1", RequestOrder: 1, ResponseOrder: 1, Compile: compiler},
				{Name: "two", Version: "v1", RequestOrder: 2, ResponseOrder: 1, Compile: compiler},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := NewRegistry(tt.defs...); err == nil {
				t.Fatal("NewRegistry() unexpectedly succeeded")
			}
		})
	}
}

func TestCompileChainUsesRegistryOrder(t *testing.T) {
	registry := mustRegistry(t,
		testDefinition("second", 20, 20),
		testDefinition("first", 10, 30),
	)
	attachments := []model.PluginAttachment{
		{Name: "second", Enabled: true, RawConfig: json.RawMessage(`{"value":"route-second"}`)},
		{Name: "first", Enabled: true, RawConfig: json.RawMessage(`{"value":"route-first"}`)},
	}
	chain, err := registry.CompileChain(nil, attachments)
	if err != nil {
		t.Fatal(err)
	}
	if got := chain.Names(); !reflect.DeepEqual(got, []string{"first", "second"}) {
		t.Fatalf("Names() = %v", got)
	}

	state := &requestctx.Context{Scratch: []any{[]string{}}}
	requestResult := chain.RunRequest(state, &http.Request{})
	if requestResult.Err != nil {
		t.Fatal(requestResult.Err)
	}
	if err := chain.RunResponse(state, &http.Response{}); err != nil {
		t.Fatal(err)
	}
	if got, want := state.Scratch[0], []string{
		"request:first:route-first",
		"request:second:route-second",
		"response:second:route-second",
		"response:first:route-first",
	}; !reflect.DeepEqual(got, want) {
		t.Fatalf("execution log = %#v, want %#v", got, want)
	}
}

func TestCompileChainRejectsUnknownAndDuplicateScopeNames(t *testing.T) {
	registry := mustRegistry(t, testDefinition("known", 1, 1))
	tests := []struct {
		name    string
		service []model.PluginAttachment
		route   []model.PluginAttachment
		want    string
	}{
		{
			name:  "unknown",
			route: []model.PluginAttachment{{Name: "unknown", Enabled: true}},
			want:  "unknown",
		},
		{
			name: "duplicate service",
			service: []model.PluginAttachment{
				{Name: "known", Enabled: true},
				{Name: "known", Enabled: true},
			},
			want: "duplicate",
		},
		{
			name: "duplicate route",
			route: []model.PluginAttachment{
				{Name: "known", Enabled: true},
				{Name: "known", Enabled: false},
			},
			want: "duplicate",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := registry.CompileChain(tt.service, tt.route)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("CompileChain() error = %v, want %q", err, tt.want)
			}
		})
	}
}

func TestPluginCompileErrorIdentifiesPlugin(t *testing.T) {
	registry := mustRegistry(t, Definition{
		Name:          "broken",
		Version:       "v1",
		RequestOrder:  1,
		ResponseOrder: 1,
		Compile: func(json.RawMessage) (CompiledPlugin, error) {
			return CompiledPlugin{}, errors.New("bad config")
		},
	})
	_, err := registry.CompileChain(nil, []model.PluginAttachment{{Name: "broken", Enabled: true}})
	if err == nil || !strings.Contains(err.Error(), "broken") || !strings.Contains(err.Error(), "bad config") {
		t.Fatalf("CompileChain() error = %v", err)
	}
}

func mustRegistry(t *testing.T, definitions ...Definition) *Registry {
	t.Helper()
	registry, err := NewRegistry(definitions...)
	if err != nil {
		t.Fatal(err)
	}
	return registry
}

func testDefinition(name string, requestOrder, responseOrder int) Definition {
	return Definition{
		Name:          name,
		Version:       "v1",
		RequestOrder:  requestOrder,
		ResponseOrder: responseOrder,
		Compile: func(raw json.RawMessage) (CompiledPlugin, error) {
			var config struct {
				Value string `json:"value"`
			}
			if len(raw) > 0 {
				if err := json.Unmarshal(raw, &config); err != nil {
					return CompiledPlugin{}, err
				}
			}
			hook := &recordingHook{name: name, value: config.Value}
			return CompiledPlugin{Request: hook, Response: hook}, nil
		},
	}
}

type recordingHook struct {
	name     string
	value    string
	request  RequestResult
	response error
}

func (h *recordingHook) OnRequest(state *requestctx.Context, _ *http.Request) RequestResult {
	appendPluginLog(state, "request:"+h.name+":"+h.value)
	return h.request
}

func (h *recordingHook) OnResponse(state *requestctx.Context, _ *http.Response) error {
	appendPluginLog(state, "response:"+h.name+":"+h.value)
	return h.response
}

func appendPluginLog(state *requestctx.Context, value string) {
	log, _ := state.Scratch[0].([]string)
	state.Scratch[0] = append(log, value)
}

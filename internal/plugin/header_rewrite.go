package plugin

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"

	"github.com/QuanTuanHuy/g-gateway/internal/requestctx"
)

type rewriteConfig struct {
	Request  rewriteDirection `json:"request"`
	Response rewriteDirection `json:"response"`
}

type rewriteDirection struct {
	Remove []string                `json:"remove"`
	Set    map[string]headerValues `json:"set"`
	Add    map[string]headerValues `json:"add"`
}

type headerValues []string

// UnmarshalJSON accepts either one header-value string or a non-empty array of
// strings and replaces v with an independently owned slice.
func (v *headerValues) UnmarshalJSON(data []byte) error {
	data = bytes.TrimSpace(data)
	if len(data) == 0 {
		return fmt.Errorf("header values must not be empty JSON")
	}
	if data[0] == '"' {
		var value string
		if err := json.Unmarshal(data, &value); err != nil {
			return err
		}
		*v = headerValues{value}
		return nil
	}
	var values []string
	if err := json.Unmarshal(data, &values); err != nil {
		return fmt.Errorf("header values must be a string or string array: %w", err)
	}
	if len(values) == 0 {
		return fmt.Errorf("header value array must not be empty")
	}
	*v = append(headerValues(nil), values...)
	return nil
}

type compiledRewrite struct {
	request  compiledDirection
	response compiledDirection
}

type compiledDirection struct {
	remove []string
	set    []headerOperation
	add    []headerOperation
}

type headerOperation struct {
	name   string
	values []string
}

func headerRewriteDefinition() Definition {
	return Definition{
		Name:          "header-rewrite",
		Version:       "1.0.0",
		RequestOrder:  200,
		ResponseOrder: 800,
		Compile: func(raw json.RawMessage) (CompiledPlugin, error) {
			var config rewriteConfig
			if err := decodeStrictPluginJSON(raw, &config); err != nil {
				return CompiledPlugin{}, err
			}
			request, err := compileRewriteDirection("request", config.Request)
			if err != nil {
				return CompiledPlugin{}, err
			}
			response, err := compileRewriteDirection("response", config.Response)
			if err != nil {
				return CompiledPlugin{}, err
			}
			compiled := &compiledRewrite{request: request, response: response}
			return CompiledPlugin{Request: compiled, Response: compiled}, nil
		},
	}
}

func compileRewriteDirection(name string, direction rewriteDirection) (compiledDirection, error) {
	var compiled compiledDirection
	seen := make(map[string]string, len(direction.Remove)+len(direction.Set)+len(direction.Add))
	for i, rawName := range direction.Remove {
		normalized, err := validateMutableHeaderName(rawName)
		if err != nil {
			return compiledDirection{}, fmt.Errorf("%s.remove[%d]: %w", name, i, err)
		}
		if previous, duplicate := seen[normalized]; duplicate {
			return compiledDirection{}, fmt.Errorf(
				"%s: header %q appears in both %s and remove",
				name,
				normalized,
				previous,
			)
		}
		seen[normalized] = "remove"
		compiled.remove = append(compiled.remove, normalized)
	}
	set, err := compileHeaderOperations(name, "set", direction.Set, seen)
	if err != nil {
		return compiledDirection{}, err
	}
	add, err := compileHeaderOperations(name, "add", direction.Add, seen)
	if err != nil {
		return compiledDirection{}, err
	}
	compiled.set = set
	compiled.add = add
	sort.Strings(compiled.remove)
	sort.Slice(compiled.set, func(i, j int) bool { return compiled.set[i].name < compiled.set[j].name })
	sort.Slice(compiled.add, func(i, j int) bool { return compiled.add[i].name < compiled.add[j].name })
	return compiled, nil
}

func compileHeaderOperations(
	directionName string,
	group string,
	raw map[string]headerValues,
	seen map[string]string,
) ([]headerOperation, error) {
	operations := make([]headerOperation, 0, len(raw))
	for rawName, rawValues := range raw {
		name, err := validateMutableHeaderName(rawName)
		if err != nil {
			return nil, fmt.Errorf("%s.%s[%q]: %w", directionName, group, rawName, err)
		}
		if previous, duplicate := seen[name]; duplicate {
			return nil, fmt.Errorf(
				"%s: header %q appears in both %s and %s",
				directionName,
				name,
				previous,
				group,
			)
		}
		if len(rawValues) == 0 {
			return nil, fmt.Errorf("%s.%s[%q]: values must not be empty", directionName, group, rawName)
		}
		values := make([]string, len(rawValues))
		for i, value := range rawValues {
			if !validStaticHeaderValue(value) {
				return nil, fmt.Errorf("%s.%s[%q][%d]: invalid header value", directionName, group, rawName, i)
			}
			values[i] = value
		}
		seen[name] = group
		operations = append(operations, headerOperation{name: name, values: values})
	}
	return operations, nil
}

func validStaticHeaderValue(value string) bool {
	for i := 0; i < len(value); i++ {
		current := value[i]
		if current == '\r' || current == '\n' || current == 0x7f || current < 0x20 && current != '\t' {
			return false
		}
	}
	return true
}

// OnRequest applies the compiled remove, set, and add operations to request
// headers in that order.
func (r *compiledRewrite) OnRequest(_ *requestctx.Context, request *http.Request) RequestResult {
	if request.Header == nil {
		request.Header = make(http.Header)
	}
	applyCompiledDirection(request.Header, r.request)
	return RequestResult{Action: Continue}
}

// OnResponse applies the compiled remove, set, and add operations to response
// headers in that order.
func (r *compiledRewrite) OnResponse(_ *requestctx.Context, response *http.Response) error {
	if response.Header == nil {
		response.Header = make(http.Header)
	}
	applyCompiledDirection(response.Header, r.response)
	return nil
}

func applyCompiledDirection(header http.Header, direction compiledDirection) {
	for _, name := range direction.remove {
		header.Del(name)
	}
	for _, operation := range direction.set {
		header[operation.name] = append([]string(nil), operation.values...)
	}
	for _, operation := range direction.add {
		for _, value := range operation.values {
			header.Add(operation.name, value)
		}
	}
}

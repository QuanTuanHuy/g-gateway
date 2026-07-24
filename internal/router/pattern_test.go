package router

import (
	"reflect"
	"strings"
	"testing"

	"github.com/QuanTuanHuy/g-gateway/internal/requestctx"
)

func TestCompilePathPatternSemantics(t *testing.T) {
	tests := []struct {
		pattern string
		path    string
		match   bool
		params  map[string]string
	}{
		{pattern: "/users", path: "/users", match: true},
		{pattern: "/users", path: "/users/", match: false},
		{pattern: "/api/*", path: "/api", match: false},
		{pattern: "/api/*", path: "/api/", match: true},
		{pattern: "/api/*", path: "/api/v1/users", match: true},
		{pattern: "/users/{id}", path: "/users/42", match: true, params: map[string]string{"id": "42"}},
		{pattern: "/users/{id}", path: "/users/", match: false},
		{pattern: "/assets/{*path}", path: "/assets", match: false},
		{pattern: "/assets/{*path}", path: "/assets/", match: true, params: map[string]string{"path": ""}},
		{pattern: "/assets/{*path}", path: "/assets/a/b", match: true, params: map[string]string{"path": "a/b"}},
	}
	for _, tt := range tests {
		t.Run(tt.pattern+"_"+tt.path, func(t *testing.T) {
			compiled, err := compilePathPattern(tt.pattern)
			if err != nil {
				t.Fatal(err)
			}
			got, params := compiled.match(tt.path)
			if got != tt.match {
				t.Fatalf("match = %v, want %v", got, tt.match)
			}
			if !reflect.DeepEqual(materializeParams(tt.path, params), tt.params) {
				t.Fatalf("params = %#v, want %#v", materializeParams(tt.path, params), tt.params)
			}
		})
	}
}

func TestCompilePathPatternRejectsInvalidPatterns(t *testing.T) {
	tests := []struct {
		pattern string
		want    string
	}{
		{pattern: "users", want: "absolute"},
		{pattern: "/users?active=true", want: "query"},
		{pattern: "/users#top", want: "query"},
		{pattern: "/users/{}", want: "parameter"},
		{pattern: "/users/{id}/{id}", want: "duplicate"},
		{pattern: "/assets/{*path}/thumb", want: "final"},
		{pattern: "/users/pre{fix}", want: "braces"},
		{pattern: "/api/v*", want: "asterisk"},
		{pattern: "/api/{*path}suffix", want: "asterisk"},
	}
	for _, tt := range tests {
		t.Run(tt.pattern, func(t *testing.T) {
			_, err := compilePathPattern(tt.pattern)
			if err == nil || !strings.Contains(strings.ToLower(err.Error()), strings.ToLower(tt.want)) {
				t.Fatalf("compilePathPattern() error = %v, want %q", err, tt.want)
			}
		})
	}
}

func TestCompilePathPatternSpecificity(t *testing.T) {
	exact, err := compilePathPattern("/users/me")
	if err != nil {
		t.Fatal(err)
	}
	parameter, err := compilePathPattern("/users/{id}")
	if err != nil {
		t.Fatal(err)
	}
	prefix, err := compilePathPattern("/users/*")
	if err != nil {
		t.Fatal(err)
	}

	if exact.specificity.kindRank <= parameter.specificity.kindRank ||
		parameter.specificity.kindRank <= prefix.specificity.kindRank {
		t.Fatalf("kind ranks exact=%d parameter=%d prefix=%d",
			exact.specificity.kindRank,
			parameter.specificity.kindRank,
			prefix.specificity.kindRank,
		)
	}
	if exact.specificity.staticSegments != 2 || exact.specificity.patternBytes != len("/users/me") {
		t.Fatalf("exact specificity = %+v", exact.specificity)
	}
}

func TestHostNormalizationAndMatching(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want string
	}{
		{name: "lowercase", raw: "API.Example.COM", want: "api.example.com"},
		{name: "remove port", raw: "API.Example.COM:8443", want: "api.example.com"},
		{name: "remove one trailing dot", raw: "API.Example.COM.", want: "api.example.com"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := NormalizeRequestHost(tt.raw)
			if err != nil {
				t.Fatal(err)
			}
			if got != tt.want {
				t.Fatalf("NormalizeRequestHost(%q) = %q, want %q", tt.raw, got, tt.want)
			}
		})
	}

	exact, err := compileHostPattern("API.Example.COM.")
	if err != nil {
		t.Fatal(err)
	}
	wildcard, err := compileHostPattern("*.example.com")
	if err != nil {
		t.Fatal(err)
	}
	if !exact.match("api.example.com") || exact.match("www.example.com") {
		t.Fatal("exact host semantics mismatch")
	}
	if !wildcard.match("api.example.com") {
		t.Fatal("wildcard did not match one left label")
	}
	if wildcard.match("example.com") {
		t.Fatal("wildcard matched apex")
	}
	if wildcard.match("v1.api.example.com") {
		t.Fatal("wildcard matched multiple left labels")
	}
}

func TestHostRejectsMalformedAuthoritiesAndPatterns(t *testing.T) {
	for _, raw := range []string{"", "[api.example.com", "api.example.com:bad", "api.example.com.."} {
		t.Run("authority_"+raw, func(t *testing.T) {
			if _, err := NormalizeRequestHost(raw); err == nil {
				t.Fatalf("NormalizeRequestHost(%q) unexpectedly succeeded", raw)
			}
		})
	}
	for _, pattern := range []string{"*.com", "*.*.example.com", "bad_host.example.com"} {
		t.Run("pattern_"+pattern, func(t *testing.T) {
			if _, err := compileHostPattern(pattern); err == nil {
				t.Fatalf("compileHostPattern(%q) unexpectedly succeeded", pattern)
			}
		})
	}
}

func materializeParams(path string, spans []requestctx.ParamSpan) map[string]string {
	if len(spans) == 0 {
		return nil
	}
	out := make(map[string]string, len(spans))
	for _, span := range spans {
		out[span.Name] = path[span.Start:span.End]
	}
	return out
}

package downstreamtls

import (
	"strings"
	"testing"
)

func FuzzNormalizeBindingHost(f *testing.F) {
	for _, seed := range []string{"api.example.com", "API.EXAMPLE.COM.", "*.example.com", "a..example"} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, raw string) {
		canonical, wildcard, err := normalizeBindingHost(raw)
		if err != nil {
			return
		}
		if canonical != strings.ToLower(canonical) || strings.HasSuffix(canonical, ".") {
			t.Fatalf("non-canonical result %q", canonical)
		}
		if wildcard && strings.Count(canonical, ".") < 1 {
			t.Fatalf("wildcard suffix too broad: %q", canonical)
		}
	})
}

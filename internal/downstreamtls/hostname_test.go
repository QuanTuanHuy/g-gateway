package downstreamtls

import (
	"strings"
	"testing"
)

func TestNormalizeBindingHost(t *testing.T) {
	tests := []struct {
		raw      string
		want     string
		wildcard bool
	}{
		{raw: "API.Example.COM.", want: "api.example.com"},
		{raw: "*.Example.COM", want: "example.com", wildcard: true},
		{raw: "a-b.example", want: "a-b.example"},
	}

	for _, test := range tests {
		got, wildcard, err := normalizeBindingHost(test.raw)
		if err != nil || got != test.want || wildcard != test.wildcard {
			t.Errorf("normalizeBindingHost(%q) = (%q, %v, %v), want (%q, %v, nil)",
				test.raw, got, wildcard, err, test.want, test.wildcard)
		}
	}
}

func TestNormalizeBindingHostRejectsInvalidNames(t *testing.T) {
	invalid := []string{
		"",
		".example.com",
		"a..example.com",
		"*.com",
		"*.*.example.com",
		"127.0.0.1",
		"münich.example",
		"bad_name.example",
		"example.com..",
		"-bad.example",
		"bad-.example",
		strings.Repeat("a", 64) + ".example",
		strings.Repeat("a", 63) + "." + strings.Repeat("b", 63) + "." +
			strings.Repeat("c", 63) + "." + strings.Repeat("d", 62),
	}

	for _, raw := range invalid {
		if _, _, err := normalizeBindingHost(raw); err == nil {
			t.Errorf("normalizeBindingHost(%q) error = nil", raw)
		}
	}
}

func TestCanonicalizeServerNameAcceptsMaximumLength(t *testing.T) {
	raw := strings.Repeat("a", 63) + "." + strings.Repeat("b", 63) + "." +
		strings.Repeat("c", 63) + "." + strings.Repeat("d", 61)
	var scratch [253]byte
	got, err := canonicalizeServerName(raw, &scratch)
	if err != nil || string(got) != raw || len(got) != 253 {
		t.Fatalf("canonicalizeServerName(max) = (%q, %v), length=%d", got, err, len(got))
	}
}

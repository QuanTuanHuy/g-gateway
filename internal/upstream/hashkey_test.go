package upstream

import (
	"net/http"
	"testing"

	"github.com/QuanTuanHuy/g-gateway/internal/model"
)

func TestHashKeyLengthPrefixesComponents(t *testing.T) {
	first := requestHash(t,
		[]model.HashKeySource{
			{Type: model.HashSourceLiteral, Value: "ab"},
			{Type: model.HashSourceLiteral, Value: "c"},
		},
	)
	second := requestHash(t,
		[]model.HashKeySource{
			{Type: model.HashSourceLiteral, Value: "a"},
			{Type: model.HashSourceLiteral, Value: "bc"},
		},
	)
	if first == second {
		t.Fatal("length framing collision")
	}
}

func TestHashKeyPreservesSourceOrder(t *testing.T) {
	first := requestHash(t, []model.HashKeySource{
		{Type: model.HashSourceLiteral, Value: "tenant"},
		{Type: model.HashSourceLiteral, Value: "session"},
	})
	second := requestHash(t, []model.HashKeySource{
		{Type: model.HashSourceLiteral, Value: "session"},
		{Type: model.HashSourceLiteral, Value: "tenant"},
	})
	if first == second {
		t.Fatal("source order did not affect hash")
	}
}

func TestHashKeyIncludesEveryHeaderValueInOrder(t *testing.T) {
	extractor := mustHashKeyExtractor(t, []model.HashKeySource{{
		Type: model.HashSourceHeader,
		Name: "X-Tenant",
	}})
	first := &http.Request{
		Header:     http.Header{"X-Tenant": {"acme", "blue"}},
		RemoteAddr: "192.0.2.1:1234",
	}
	second := &http.Request{
		Header:     http.Header{"X-Tenant": {"blue", "acme"}},
		RemoteAddr: "192.0.2.1:1234",
	}
	combined := &http.Request{
		Header:     http.Header{"X-Tenant": {"acme,blue"}},
		RemoteAddr: "192.0.2.1:1234",
	}
	firstHash, fallback := extractor.sum64(first)
	if fallback {
		t.Fatal("present header used fallback")
	}
	secondHash, _ := extractor.sum64(second)
	combinedHash, _ := extractor.sum64(combined)
	if firstHash == secondHash || firstHash == combinedHash {
		t.Fatal("header value framing or order was lost")
	}
}

func TestHashKeyUsesFirstValidExactCookie(t *testing.T) {
	extractor := mustHashKeyExtractor(t, []model.HashKeySource{{
		Type: model.HashSourceCookie,
		Name: "session_id",
	}})
	first := &http.Request{
		Header: http.Header{"Cookie": {
			"other=x; session_id=first",
			"session_id=second",
		}},
		RemoteAddr: "192.0.2.1:1234",
	}
	second := &http.Request{
		Header:     http.Header{"Cookie": {"session_id=first"}},
		RemoteAddr: "198.51.100.2:4321",
	}
	firstHash, fallback := extractor.sum64(first)
	if fallback {
		t.Fatal("valid cookie used fallback")
	}
	secondHash, _ := extractor.sum64(second)
	if firstHash != secondHash {
		t.Fatal("extractor did not use first valid exact-name cookie")
	}
}

func TestHashKeyCompoundMissingMarkersPreventAmbiguity(t *testing.T) {
	extractor := mustHashKeyExtractor(t, []model.HashKeySource{
		{Type: model.HashSourceHeader, Name: "X-A"},
		{Type: model.HashSourceHeader, Name: "X-B"},
	})
	first := &http.Request{
		Header:     http.Header{"X-A": {"value"}},
		RemoteAddr: "192.0.2.1:1234",
	}
	second := &http.Request{
		Header:     http.Header{"X-B": {"value"}},
		RemoteAddr: "192.0.2.1:1234",
	}
	firstHash, firstFallback := extractor.sum64(first)
	secondHash, secondFallback := extractor.sum64(second)
	if firstFallback || secondFallback {
		t.Fatal("one present compound component must prevent fallback")
	}
	if firstHash == secondHash {
		t.Fatal("missing component positions collided")
	}
}

func TestHashKeyFallsBackOnlyWhenAllDynamicSourcesAreMissing(t *testing.T) {
	extractor := mustHashKeyExtractor(t, []model.HashKeySource{
		{Type: model.HashSourceHeader, Name: "X-Tenant"},
		{Type: model.HashSourceCookie, Name: "session_id"},
	})
	missing := &http.Request{
		Header:     make(http.Header),
		RemoteAddr: "192.0.2.1:1234",
	}
	_, fallback := extractor.sum64(missing)
	if !fallback {
		t.Fatal("all-missing dynamic key did not fall back")
	}

	withLiteral := mustHashKeyExtractor(t, []model.HashKeySource{
		{Type: model.HashSourceHeader, Name: "X-Tenant"},
		{Type: model.HashSourceLiteral, Value: "fixed"},
	})
	_, fallback = withLiteral.sum64(missing)
	if fallback {
		t.Fatal("literal component must prevent fallback")
	}
}

func TestHashKeyNormalizesImmediatePeerWithoutForwardingHeaders(t *testing.T) {
	extractor := mustHashKeyExtractor(t, []model.HashKeySource{{
		Type: model.HashSourceRemoteAddr,
	}})
	ipv4 := &http.Request{
		Header: http.Header{
			"X-Forwarded-For": {"203.0.113.9"},
		},
		RemoteAddr: "192.0.2.1:1234",
	}
	mapped := &http.Request{
		Header:     make(http.Header),
		RemoteAddr: "[::ffff:192.0.2.1]:4321",
	}
	first, fallback := extractor.sum64(ipv4)
	if fallback {
		t.Fatal("explicit remote_addr source reported fallback")
	}
	second, _ := extractor.sum64(mapped)
	if first != second {
		t.Fatal("equivalent immediate peer addresses hashed differently")
	}
}

func TestCompileHashKeyRejectsBypassedInvalidPolicy(t *testing.T) {
	for _, policy := range []model.HashKeyPolicy{
		{},
		{Sources: []model.HashKeySource{{Type: model.HashSourceHeader, Name: "bad header"}}},
		{Sources: []model.HashKeySource{{Type: model.HashSourceLiteral}}},
	} {
		if _, err := compileHashKey(policy); err == nil {
			t.Fatalf("compileHashKey(%+v) error = nil", policy)
		}
	}
}

func requestHash(t *testing.T, sources []model.HashKeySource) uint64 {
	t.Helper()
	extractor := mustHashKeyExtractor(t, sources)
	request := &http.Request{
		Header:     make(http.Header),
		RemoteAddr: "192.0.2.1:1234",
	}
	sum, fallback := extractor.sum64(request)
	if fallback {
		t.Fatal("literal key unexpectedly used fallback")
	}
	return sum
}

func mustHashKeyExtractor(t testing.TB, sources []model.HashKeySource) hashKeyExtractor {
	t.Helper()
	extractor, err := compileHashKey(model.HashKeyPolicy{Sources: sources})
	if err != nil {
		t.Fatal(err)
	}
	return extractor
}

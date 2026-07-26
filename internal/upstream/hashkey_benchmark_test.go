package upstream

import (
	"net/http"
	"testing"

	"github.com/QuanTuanHuy/g-gateway/internal/model"
)

var (
	benchmarkHashKeySum      uint64
	benchmarkHashKeyFallback bool
)

func BenchmarkHashKey(b *testing.B) {
	cases := []struct {
		name    string
		sources []model.HashKeySource
		request *http.Request
	}{
		{
			name: "header",
			sources: []model.HashKeySource{{
				Type: model.HashSourceHeader,
				Name: "X-Tenant",
			}},
			request: &http.Request{
				Header:     http.Header{"X-Tenant": {"acme", "blue"}},
				RemoteAddr: "192.0.2.1:1234",
			},
		},
		{
			name: "cookie",
			sources: []model.HashKeySource{{
				Type: model.HashSourceCookie,
				Name: "session_id",
			}},
			request: &http.Request{
				Header:     http.Header{"Cookie": {"other=x; session_id=abc"}},
				RemoteAddr: "192.0.2.1:1234",
			},
		},
		{
			name: "compound",
			sources: []model.HashKeySource{
				{Type: model.HashSourceHeader, Name: "X-Tenant"},
				{Type: model.HashSourceCookie, Name: "session_id"},
				{Type: model.HashSourceLiteral, Value: "v1"},
			},
			request: &http.Request{
				Header: http.Header{
					"X-Tenant": {"acme"},
					"Cookie":   {"session_id=abc"},
				},
				RemoteAddr: "192.0.2.1:1234",
			},
		},
		{
			name: "fallback",
			sources: []model.HashKeySource{
				{Type: model.HashSourceHeader, Name: "X-Tenant"},
				{Type: model.HashSourceCookie, Name: "session_id"},
			},
			request: &http.Request{
				Header:     make(http.Header),
				RemoteAddr: "[::ffff:192.0.2.1]:1234",
			},
		},
	}

	for _, benchmark := range cases {
		b.Run(benchmark.name, func(b *testing.B) {
			extractor := mustHashKeyExtractor(b, benchmark.sources)
			b.ReportAllocs()
			b.ResetTimer()
			for b.Loop() {
				benchmarkHashKeySum, benchmarkHashKeyFallback = extractor.sum64(benchmark.request)
			}
		})
	}
}

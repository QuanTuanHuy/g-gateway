package upstream

import (
	"testing"

	"github.com/QuanTuanHuy/g-gateway/internal/model"
)

func FuzzNormalizeEndpoint(f *testing.F) {
	for _, seed := range []string{
		"http://example.com",
		"http://127.0.0.1:8080/",
		"http://[::1]:80",
		"http://user@example.com",
	} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, raw string) {
		resources := []model.Upstream{validUpstreamWith(model.Endpoint{
			URL:    raw,
			Weight: 1,
		})}
		_, _ = Normalize(resources)
	})
}

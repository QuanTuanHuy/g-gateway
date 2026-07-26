package upstream

import (
	"testing"

	"github.com/QuanTuanHuy/g-gateway/internal/model"
)

func TestEndpointIdentityIgnoresWeightAndIncludesUpstreamID(t *testing.T) {
	first, err := newEndpointRuntime("users", model.Endpoint{
		URL:    "http://example.com:80",
		Weight: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	reweighted, err := newEndpointRuntime("users", model.Endpoint{
		URL:    "http://example.com:80",
		Weight: 100,
	})
	if err != nil {
		t.Fatal(err)
	}
	otherUpstream, err := newEndpointRuntime("orders", model.Endpoint{
		URL:    "http://example.com:80",
		Weight: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if first.identity != reweighted.identity {
		t.Fatalf("weight changed identity: %q != %q", first.identity, reweighted.identity)
	}
	if first.identity == otherUpstream.identity {
		t.Fatalf("upstream ID missing from identity: %q", first.identity)
	}
}

func TestEndpointRuntimeParsesAndRetainsCanonicalTarget(t *testing.T) {
	runtime, err := newEndpointRuntime("users", model.Endpoint{
		URL:    "http://example.com:80",
		Weight: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if runtime.target.Scheme != "http" || runtime.target.Host != "example.com:80" {
		t.Fatalf("target = %s", runtime.target)
	}
}

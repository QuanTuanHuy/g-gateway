package upstream

import (
	"errors"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/QuanTuanHuy/g-gateway/internal/model"
)

func TestTableBuildsAndLooksUpRuntimes(t *testing.T) {
	resources := []model.Upstream{
		testTableResource("second", "http://two:8080"),
		testTableResource("baseline", "http://one:8080"),
	}
	table, err := NewTable(resources)
	if err != nil {
		t.Fatal(err)
	}
	defer table.CloseIdleConnections()

	runtime, ok := table.Get("baseline")
	if !ok || runtime.Target().String() != "http://one:8080" {
		t.Fatalf("Get(baseline) = %v, %v", runtime, ok)
	}
	if _, ok := table.Get("absent"); ok {
		t.Fatal("Get(absent) unexpectedly succeeded")
	}
}

func TestTableRejectsChangedUpstreamSet(t *testing.T) {
	resources := []model.Upstream{testTableResource("baseline", "http://one:8080")}
	table, err := NewTable(resources)
	if err != nil {
		t.Fatal(err)
	}
	defer table.CloseIdleConnections()

	changed := model.CloneResourceSet(model.ResourceSet{Upstreams: resources}).Upstreams
	changed[0].Endpoints[0].URL = "http://two:8080"
	err = table.ValidateResources(changed)
	if err == nil || !strings.Contains(err.Error(), "UPSTREAM_SET_IMMUTABLE") {
		t.Fatalf("ValidateResources() error = %v", err)
	}
}

func TestTableAcceptsReorderedEqualUpstreamSet(t *testing.T) {
	first := testTableResource("first", "http://one:8080")
	second := testTableResource("second", "http://two:8080")
	table, err := NewTable([]model.Upstream{first, second})
	if err != nil {
		t.Fatal(err)
	}
	defer table.CloseIdleConnections()

	if err := table.ValidateResources([]model.Upstream{second, first}); err != nil {
		t.Fatalf("ValidateResources() error = %v", err)
	}
}

func TestTableRejectsInvalidResourceIDs(t *testing.T) {
	tests := []struct {
		name      string
		resources []model.Upstream
		want      string
	}{
		{name: "empty set", want: "at least one"},
		{name: "missing ID", resources: []model.Upstream{testTableResource("", "http://one:8080")}, want: "id"},
		{
			name: "duplicate ID",
			resources: []model.Upstream{
				testTableResource("same", "http://one:8080"),
				testTableResource("same", "http://two:8080"),
			},
			want: "duplicate",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			table, err := NewTable(tt.resources)
			if err == nil || !strings.Contains(strings.ToLower(err.Error()), strings.ToLower(tt.want)) {
				if table != nil {
					table.CloseIdleConnections()
				}
				t.Fatalf("NewTable() = %v, %v; want error containing %q", table, err, tt.want)
			}
		})
	}
}

func TestTableClosesConstructedRuntimesAfterPartialFailure(t *testing.T) {
	original := newRuntime
	t.Cleanup(func() { newRuntime = original })

	calls := 0
	closed := 0
	newRuntime = func(resource model.Upstream) (*Runtime, error) {
		calls++
		if calls == 2 {
			return nil, errors.New("construction failed")
		}
		target, err := url.Parse(resource.Endpoints[0].URL)
		if err != nil {
			return nil, err
		}
		return &Runtime{
			endpoint: &endpointRuntime{target: target},
			transport: &transportRuntime{
				closeIdleConnections: func() {
					closed++
				},
			},
		}, nil
	}

	_, err := NewTable([]model.Upstream{
		testTableResource("first", "http://one:8080"),
		testTableResource("second", "http://two:8080"),
	})
	if err == nil || !strings.Contains(err.Error(), "construction failed") {
		t.Fatalf("NewTable() error = %v", err)
	}
	if closed != 1 {
		t.Fatalf("closed runtimes = %d, want 1", closed)
	}
}

func testTableResource(id, endpoint string) model.Upstream {
	return model.Upstream{
		ID:        id,
		Endpoints: []model.Endpoint{{URL: endpoint, Weight: 1}},
		Balancer:  model.BalancerPolicy{Type: model.BalancerWeightedRoundRobin},
		Transport: model.TransportConfig{
			DialTimeout:               time.Second,
			ResponseHeaderTimeout:     2 * time.Second,
			IdleConnectionTimeout:     3 * time.Second,
			MaxIdleConnections:        10,
			MaxIdleConnectionsPerHost: 5,
		},
	}
}

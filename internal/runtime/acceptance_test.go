package runtime

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	goruntime "runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/QuanTuanHuy/g-gateway/internal/benchdataset"
	"github.com/QuanTuanHuy/g-gateway/internal/model"
	"github.com/QuanTuanHuy/g-gateway/internal/plugin"
	"github.com/QuanTuanHuy/g-gateway/internal/upstream"
)

func TestPhase2Acceptance(t *testing.T) {
	expensive := os.Getenv("GATEWAY_PHASE2_ACCEPTANCE") == "1"
	routeCount := 10_000
	swapCount := 2
	if expensive {
		routeCount = 100_000
		swapCount = 20
	}
	resources, metadata, err := benchdataset.Generate(benchdataset.Options{
		RouteCount: routeCount,
		Seed:       20260723,
		Endpoint:   "http://upstream-performance:8080",
	})
	if err != nil {
		t.Fatal(err)
	}
	upstreamRegistry, err := upstream.NewRegistry(64, nil)
	if err != nil {
		t.Fatal(err)
	}
	pluginRegistry, err := plugin.NewBuiltinRegistry()
	if err != nil {
		t.Fatal(err)
	}
	builder, err := NewBuilder(pluginRegistry)
	if err != nil {
		t.Fatal(err)
	}
	manager := NewManager(builder, upstreamRegistry, nil)
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		if err := manager.Close(ctx); err != nil {
			t.Errorf("Manager.Close() error = %v", err)
		}
	}()

	goruntime.GC()
	var baseline goruntime.MemStats
	goruntime.ReadMemStats(&baseline)
	started := time.Now()
	if err := manager.Apply(1, resources); err != nil {
		t.Fatal(err)
	}
	compileElapsed := time.Since(started)
	goruntime.GC()
	var oneSnapshot goruntime.MemStats
	goruntime.ReadMemStats(&oneSnapshot)
	oneSnapshotHeap := heapDelta(oneSnapshot.HeapAlloc, baseline.HeapAlloc)

	mutableRoute := firstGeneratedRoute(resources.Routes)
	for swap := 0; swap < swapCount; swap++ {
		revision := model.CloneResourceSet(resources)
		revision.Routes[mutableRoute].Plugins = []model.PluginAttachment{{
			Name:    "header-rewrite",
			Enabled: true,
			RawConfig: json.RawMessage(`{"response":{"set":{"X-Acceptance-Revision":"` +
				strconv.Itoa(swap+2) +
				`"}}}`),
		}}
		if err := manager.Apply(uint64(swap+2), revision); err != nil {
			t.Fatalf("Apply(%d) error = %v", swap+2, err)
		}
		revision = model.ResourceSet{}
	}
	goruntime.GC()
	goruntime.GC()
	var steadyState goruntime.MemStats
	goruntime.ReadMemStats(&steadyState)
	steadyStateHeap := heapDelta(steadyState.HeapAlloc, baseline.HeapAlloc)

	active := manager.Load()
	if active == nil || active.Revision() != uint64(swapCount+1) {
		t.Fatalf("active snapshot = %#v, want revision %d", active, swapCount+1)
	}
	for _, position := range []string{"first", "middle", "last"} {
		sentinel := metadata.Sentinels[position]
		request := httptest.NewRequest(http.MethodGet, sentinel.URL, nil)
		match, err := active.Match(request)
		if err != nil || !match.Found || match.Route.Meta().ID != sentinel.RouteID {
			t.Fatalf("%s sentinel match = %+v, %v", position, match, err)
		}
	}

	t.Logf(
		"routes=%d swaps=%d compile=%s one_snapshot_heap=%d steady_state_heap=%d go=%s cpus=%d checksum=%s seed=%d",
		routeCount,
		swapCount,
		compileElapsed,
		oneSnapshotHeap,
		steadyStateHeap,
		goruntime.Version(),
		goruntime.NumCPU(),
		metadata.Checksum,
		metadata.Seed,
	)
	if !expensive {
		return
	}
	if compileElapsed > 5*time.Second {
		t.Fatalf("100,000-route compile time = %s, want <= 5s", compileElapsed)
	}
	if oneSnapshotHeap > 512<<20 {
		t.Fatalf("active snapshot heap = %d bytes, want <= 512 MiB", oneSnapshotHeap)
	}
	if oneSnapshotHeap == 0 {
		t.Fatal("active snapshot heap delta is zero")
	}
	if steadyStateHeap > oneSnapshotHeap*115/100 {
		t.Fatalf(
			"steady-state heap = %d bytes, want <= 115%% of one snapshot (%d bytes)",
			steadyStateHeap,
			oneSnapshotHeap,
		)
	}
}

func firstGeneratedRoute(routes []model.Route) int {
	for index := range routes {
		if strings.HasPrefix(routes[index].ID, "route-") {
			return index
		}
	}
	return 0
}

func heapDelta(after, before uint64) uint64 {
	if after <= before {
		return 0
	}
	return after - before
}

package upstream

import (
	"slices"
	"testing"
)

func TestWRRExactDistribution(t *testing.T) {
	selector, err := compileWRR(
		testWeightedEndpoints(5, 2, 1),
		&selectionState{},
	)
	if err != nil {
		t.Fatal(err)
	}
	counts := make([]int, 3)
	for range 8 {
		counts[selector.selectIndex()]++
	}
	if !slices.Equal(counts, []int{5, 2, 1}) {
		t.Fatalf("distribution = %v, want [5 2 1]", counts)
	}
}

func TestWRRBreaksDeadlineTiesByIdentity(t *testing.T) {
	selector, err := compileWRR([]weightedEndpoint{
		{identity: "b", weight: 1},
		{identity: "a", weight: 1},
	}, &selectionState{})
	if err != nil {
		t.Fatal(err)
	}
	got := []uint32{selector.selectIndex(), selector.selectIndex()}
	if !slices.Equal(got, []uint32{1, 0}) {
		t.Fatalf("schedule = %v, want [1 0]", got)
	}
}

func TestWRRUsesSingleEndpointFastPathAndExcludesZeroWeights(t *testing.T) {
	selector, err := compileWRR([]weightedEndpoint{
		{identity: "disabled", weight: 0},
		{identity: "active", weight: 10},
	}, &selectionState{})
	if err != nil {
		t.Fatal(err)
	}
	if selector.schedule != nil {
		t.Fatalf("schedule = %v, want nil fast path", selector.schedule)
	}
	for range 10 {
		if got := selector.selectIndex(); got != 1 {
			t.Fatalf("index = %d, want 1", got)
		}
	}
}

func TestWRRCapsScheduleAndKeepsEveryActiveEndpoint(t *testing.T) {
	endpoints := make([]weightedEndpoint, MaxUpstreamEndpoints)
	for i := range endpoints {
		endpoints[i] = weightedEndpoint{
			identity: string(rune(0x1000 + i)),
			weight:   uint32(i + 1),
		}
	}
	selector, err := compileWRR(endpoints, &selectionState{})
	if err != nil {
		t.Fatal(err)
	}
	if len(selector.schedule) != MaxWRRSchedule {
		t.Fatalf("schedule length = %d, want %d", len(selector.schedule), MaxWRRSchedule)
	}
	seen := make([]bool, len(endpoints))
	for _, index := range selector.schedule {
		seen[index] = true
	}
	for index, selected := range seen {
		if !selected {
			t.Fatalf("endpoint %d has no schedule slot", index)
		}
	}
}

func TestWRRRejectsNoActiveEndpoint(t *testing.T) {
	_, err := compileWRR(testWeightedEndpoints(0, 0), &selectionState{})
	if err == nil {
		t.Fatal("compileWRR() error = nil")
	}
}

func testWeightedEndpoints(weights ...uint32) []weightedEndpoint {
	endpoints := make([]weightedEndpoint, len(weights))
	for index, weight := range weights {
		endpoints[index] = weightedEndpoint{
			identity: string(rune('a' + index)),
			weight:   weight,
		}
	}
	return endpoints
}

package upstream

import (
	"encoding/binary"
	"math"
	"slices"
	"strconv"
	"testing"

	"github.com/cespare/xxhash/v2"
)

func TestContinuumIsStableAcrossCompiles(t *testing.T) {
	endpoints := testWeightedEndpoints(1, 2, 3)
	first, err := compileContinuum(endpoints)
	if err != nil {
		t.Fatal(err)
	}
	second, err := compileContinuum(endpoints)
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"tenant-a", "tenant-b", "tenant-c"} {
		sum := xxhash.Sum64String(key)
		if first.selectIndex(sum) != second.selectIndex(sum) {
			t.Fatalf("selection changed for %q", key)
		}
	}
}

func TestContinuumPointEncoding(t *testing.T) {
	framed := []byte{6, 'n', 'o', 'd', 'e', '-', 'a', 0, 0, 0, 0, 0, 0, 0, 42}
	want := xxhash.Sum64(framed)
	if got := continuumPointHash("node-a", 42); got != want {
		t.Fatalf("point hash = %x, want %x", got, want)
	}
}

func TestContinuumSortsHashesAndBreaksCollisionsDeterministically(t *testing.T) {
	points := []continuumPoint{
		{hash: 9, identity: "b", virtualIndex: 0, endpointIndex: 1},
		{hash: 2, identity: "z", virtualIndex: 0, endpointIndex: 2},
		{hash: 9, identity: "a", virtualIndex: 2, endpointIndex: 0},
		{hash: 9, identity: "a", virtualIndex: 1, endpointIndex: 0},
	}
	sortContinuumPoints(points)
	if points[0].hash != 2 ||
		points[1].identity != "a" || points[1].virtualIndex != 1 ||
		points[2].identity != "a" || points[2].virtualIndex != 2 ||
		points[3].identity != "b" {
		t.Fatalf("points = %+v", points)
	}
}

func TestContinuumUsesSingleEndpointFastPathAndExcludesZeroWeights(t *testing.T) {
	continuum, err := compileContinuum([]weightedEndpoint{
		{identity: "disabled", weight: 0},
		{identity: "active", weight: 10},
	})
	if err != nil {
		t.Fatal(err)
	}
	if continuum.hashes != nil || continuum.indexes != nil {
		t.Fatalf("continuum = %+v, want direct fast path", continuum)
	}
	for _, sum := range []uint64{0, 1, ^uint64(0)} {
		if got := continuum.selectIndex(sum); got != 1 {
			t.Fatalf("index = %d, want 1", got)
		}
	}
}

func TestContinuumCapsPointsAndKeepsEveryActiveEndpoint(t *testing.T) {
	endpoints := make([]weightedEndpoint, MaxUpstreamEndpoints)
	for index := range endpoints {
		endpoints[index] = weightedEndpoint{
			identity: "endpoint-" + strconv.Itoa(index),
			weight:   uint32(index + 1),
		}
	}
	continuum, err := compileContinuum(endpoints)
	if err != nil {
		t.Fatal(err)
	}
	if len(continuum.hashes) != MaxContinuumPoints {
		t.Fatalf("points = %d, want %d", len(continuum.hashes), MaxContinuumPoints)
	}
	if !slices.IsSorted(continuum.hashes) {
		t.Fatal("continuum hashes are not sorted")
	}
	seen := make([]bool, len(endpoints))
	for _, index := range continuum.indexes {
		seen[index] = true
	}
	for index, selected := range seen {
		if !selected {
			t.Fatalf("endpoint %d has no continuum point", index)
		}
	}
}

func TestConsistentHashDistributionTracksWeights(t *testing.T) {
	continuum, err := compileContinuum(testWeightedEndpoints(1, 2, 3))
	if err != nil {
		t.Fatal(err)
	}
	const keys = 1_000_000
	counts := make([]int, 3)
	var encoded [8]byte
	for key := range keys {
		binary.BigEndian.PutUint64(encoded[:], uint64(key))
		counts[continuum.selectIndex(xxhash.Sum64(encoded[:]))]++
	}
	exact := continuumRingShares(continuum, 3)
	want := []float64{1.0 / 6, 2.0 / 6, 3.0 / 6}
	for index, count := range counts {
		got := float64(count) / keys
		if math.Abs(got-exact[index]) > 0.005 {
			t.Fatalf("distribution = %v; endpoint %d sample %.4f, exact ring %.4f", counts, index, got, exact[index])
		}
		if math.Abs(exact[index]-want[index]) > 0.06 {
			t.Fatalf("point counts = %v; endpoint %d exact ring %.4f, want %.4f ±0.06", continuumPointCounts(continuum, 3), index, exact[index], want[index])
		}
	}
}

func continuumPointCounts(compiled continuum, endpointCount int) []int {
	counts := make([]int, endpointCount)
	for _, index := range compiled.indexes {
		counts[index]++
	}
	return counts
}

func continuumRingShares(compiled continuum, endpointCount int) []float64 {
	shares := make([]float64, endpointCount)
	const ringSize = 18446744073709551616.0
	for index, current := range compiled.hashes {
		previous := compiled.hashes[(index+len(compiled.hashes)-1)%len(compiled.hashes)]
		var width float64
		if current >= previous {
			width = float64(current - previous)
		} else {
			width = float64(^uint64(0)-previous) + float64(current) + 1
		}
		shares[compiled.indexes[index]] += width / ringSize
	}
	return shares
}

func TestConsistentHashRemapIsBoundedWhenEndpointChanges(t *testing.T) {
	threeEndpoints := testWeightedEndpoints(1, 1, 1)
	fourEndpoints := testWeightedEndpoints(1, 1, 1, 1)
	three, err := compileContinuum(threeEndpoints)
	if err != nil {
		t.Fatal(err)
	}
	four, err := compileContinuum(fourEndpoints)
	if err != nil {
		t.Fatal(err)
	}

	const keys = 200_000
	var encoded [8]byte
	addedRemaps := 0
	removedRemaps := 0
	for key := range keys {
		binary.BigEndian.PutUint64(encoded[:], uint64(key))
		sum := xxhash.Sum64(encoded[:])
		threeIndex := three.selectIndex(sum)
		fourIndex := four.selectIndex(sum)
		if threeEndpoints[threeIndex].identity != fourEndpoints[fourIndex].identity {
			addedRemaps++
		}
		if fourEndpoints[fourIndex].identity != threeEndpoints[threeIndex].identity {
			removedRemaps++
		}
	}
	for name, remaps := range map[string]int{"add": addedRemaps, "remove": removedRemaps} {
		ratio := float64(remaps) / keys
		if ratio < 0.15 || ratio > 0.35 {
			t.Fatalf("%s remap ratio = %.4f, want 0.15..0.35", name, ratio)
		}
	}
}

func TestContinuumRejectsNoActiveEndpoint(t *testing.T) {
	if _, err := compileContinuum(testWeightedEndpoints(0, 0)); err == nil {
		t.Fatal("compileContinuum() error = nil")
	}
}

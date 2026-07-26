package upstream

import (
	"strconv"
	"testing"
)

var benchmarkWRRIndex uint32

func BenchmarkWRRSelect(b *testing.B) {
	for _, endpointCount := range []int{1, 2, 100, 1000} {
		b.Run(strconv.Itoa(endpointCount), func(b *testing.B) {
			selector := mustBenchmarkWRR(b, endpointCount)
			b.ReportAllocs()
			b.ResetTimer()
			for b.Loop() {
				benchmarkWRRIndex = selector.selectIndex()
			}
		})
	}
}

func mustBenchmarkWRR(b *testing.B, endpointCount int) wrrSelector {
	b.Helper()
	endpoints := make([]weightedEndpoint, endpointCount)
	for index := range endpoints {
		endpoints[index] = weightedEndpoint{
			identity: strconv.Itoa(index),
			weight:   uint32(index%5 + 1),
		}
	}
	selector, err := compileWRR(endpoints, &selectionState{})
	if err != nil {
		b.Fatal(err)
	}
	return selector
}

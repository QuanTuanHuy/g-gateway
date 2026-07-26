package upstream

import (
	"encoding/binary"
	"strconv"
	"testing"

	"github.com/cespare/xxhash/v2"
)

var benchmarkConsistentHashIndex uint32

func BenchmarkConsistentHashSelect(b *testing.B) {
	var (
		hashes  [1024]uint64
		encoded [8]byte
	)
	for index := range hashes {
		binary.BigEndian.PutUint64(encoded[:], uint64(index))
		hashes[index] = xxhash.Sum64(encoded[:])
	}

	for _, endpointCount := range []int{1, 10, 100, 1000} {
		b.Run(strconv.Itoa(endpointCount), func(b *testing.B) {
			endpoints := make([]weightedEndpoint, endpointCount)
			for index := range endpoints {
				endpoints[index] = weightedEndpoint{
					identity: strconv.Itoa(index),
					weight:   uint32(index%5 + 1),
				}
			}
			continuum, err := compileContinuum(endpoints)
			if err != nil {
				b.Fatal(err)
			}
			index := 0
			b.ReportAllocs()
			b.ResetTimer()
			for b.Loop() {
				benchmarkConsistentHashIndex = continuum.selectIndex(hashes[index&1023])
				index++
			}
		})
	}
}

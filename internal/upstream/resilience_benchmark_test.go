package upstream

import (
	"strconv"
	"testing"

	"github.com/QuanTuanHuy/g-gateway/internal/model"
)

var benchmarkHealthOrdinal uint32

func BenchmarkHealthAwareWRR(b *testing.B) {
	selector := mustBenchmarkWRR(b, 100)
	b.Run("all-healthy", func(b *testing.B) {
		b.ReportAllocs()
		for b.Loop() {
			benchmarkHealthOrdinal, _ = selector.selectNext(func(uint32) bool { return true })
		}
	})
	b.Run("one-unhealthy", func(b *testing.B) {
		b.ReportAllocs()
		for b.Loop() {
			benchmarkHealthOrdinal, _ = selector.selectNext(func(ordinal uint32) bool { return ordinal != 0 })
		}
	})
}

func BenchmarkHealthAwareConsistentHash(b *testing.B) {
	endpoints := make([]weightedEndpoint, 100)
	for index := range endpoints {
		endpoints[index] = weightedEndpoint{identity: strconv.Itoa(index), weight: 1}
	}
	continuum, err := compileContinuum(endpoints)
	if err != nil {
		b.Fatal(err)
	}
	b.Run("all-healthy", func(b *testing.B) {
		b.ReportAllocs()
		for b.Loop() {
			benchmarkHealthOrdinal, _ = continuum.selectNext(42, len(endpoints), func(uint32) bool { return true })
		}
	})
	b.Run("one-unhealthy", func(b *testing.B) {
		b.ReportAllocs()
		for b.Loop() {
			benchmarkHealthOrdinal, _ = continuum.selectNext(42, len(endpoints), func(ordinal uint32) bool { return ordinal != continuum.selectIndex(42) })
		}
	})
}

func BenchmarkRetryBudget(b *testing.B) {
	budget := newRetryBudget(model.RetryBudgetPolicy{RatioPer1000: 1000, Burst: 1000, MaxInflight: 1000})
	b.Run("credit", func(b *testing.B) {
		b.ReportAllocs()
		for b.Loop() {
			budget.CreditPrimary()
		}
	})
	b.Run("acquire-release", func(b *testing.B) {
		b.ReportAllocs()
		for b.Loop() {
			budget.CreditPrimary()
			permit, ok := budget.Acquire()
			if !ok {
				b.Fatal("permit denied")
			}
			permit.Release()
		}
	})
}

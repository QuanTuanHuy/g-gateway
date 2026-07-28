package upstream

import (
	"sync"
	"sync/atomic"
	"testing"

	"github.com/QuanTuanHuy/g-gateway/internal/model"
)

func TestRetryBudgetCreditsAndCaps(t *testing.T) {
	budget := newRetryBudget(model.RetryBudgetPolicy{
		RatioPer1000: 100,
		Burst:        2,
		MaxInflight:  1,
	})
	for range 20 {
		budget.CreditPrimary()
	}
	first, ok := budget.Acquire()
	if !ok {
		t.Fatal("first retry denied")
	}
	if _, ok := budget.Acquire(); ok {
		t.Fatal("inflight cap exceeded")
	}
	first.Release()
	second, ok := budget.Acquire()
	if !ok {
		t.Fatal("second retry denied")
	}
	second.Release()
	if _, ok := budget.Acquire(); ok {
		t.Fatal("token cap exceeded")
	}
}

func TestRetryPermitPanicsOnDoubleRelease(t *testing.T) {
	budget := newRetryBudget(model.RetryBudgetPolicy{RatioPer1000: 1000, Burst: 1, MaxInflight: 1})
	budget.CreditPrimary()
	permit, ok := budget.Acquire()
	if !ok {
		t.Fatal("retry denied")
	}
	permit.Release()
	defer func() {
		if recover() == nil {
			t.Fatal("double release did not panic")
		}
	}()
	permit.Release()
}

func TestRetryBudgetConcurrent(t *testing.T) {
	const maxInflight = 8
	budget := newRetryBudget(model.RetryBudgetPolicy{
		RatioPer1000: 1000,
		Burst:        100,
		MaxInflight:  maxInflight,
	})
	for range 100 {
		budget.CreditPrimary()
	}

	start := make(chan struct{})
	release := make(chan struct{})
	var acquired atomic.Int32
	var wg sync.WaitGroup
	for range 100 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			permit, ok := budget.Acquire()
			if !ok {
				return
			}
			acquired.Add(1)
			<-release
			permit.Release()
		}()
	}
	close(start)
	for acquired.Load() < maxInflight {
		if budget.MaxObservedInflight() > maxInflight {
			t.Fatalf("max observed inflight = %d", budget.MaxObservedInflight())
		}
	}
	close(release)
	wg.Wait()
	if budget.MaxObservedInflight() > maxInflight {
		t.Fatalf("max observed inflight = %d", budget.MaxObservedInflight())
	}
	if budget.Inflight() != 0 {
		t.Fatalf("inflight = %d", budget.Inflight())
	}
	if budget.Credits() > 100*1000 {
		t.Fatalf("credits = %d", budget.Credits())
	}
}

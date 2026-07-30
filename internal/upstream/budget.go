package upstream

import (
	"sync/atomic"

	"github.com/QuanTuanHuy/g-gateway/internal/model"
)

const retryCreditUnit = uint64(1000)

type retryBudget struct {
	ratio       uint64
	maxCredits  uint64
	maxInflight uint32
	credits     atomic.Uint64
	inflight    atomic.Uint32
	maxObserved atomic.Uint32
}

// RetryPermit owns one in-flight retry slot. Its zero value is invalid; every
// successfully acquired permit must be released exactly once.
type RetryPermit struct {
	budget   *retryBudget
	released atomic.Bool
}

func newRetryBudget(policy model.RetryBudgetPolicy) *retryBudget {
	return &retryBudget{
		ratio:       uint64(policy.RatioPer1000),
		maxCredits:  uint64(policy.Burst) * retryCreditUnit,
		maxInflight: uint32(policy.MaxInflight),
	}
}

// CreditPrimary adds the configured fixed-point credits for one primary
// request, saturating at the burst cap. It is safe for concurrent use.
func (b *retryBudget) CreditPrimary() {
	for {
		current := b.credits.Load()
		if current >= b.maxCredits {
			return
		}
		next := current + b.ratio
		if next > b.maxCredits {
			next = b.maxCredits
		}
		if b.credits.CompareAndSwap(current, next) {
			return
		}
	}
}

// Acquire reserves an in-flight retry slot and consumes one whole retry token.
// It fails without retaining a slot when either bound is unavailable. A nil or
// disabled budget is represented by callers as no acquisition.
func (b *retryBudget) Acquire() (RetryPermit, bool) {
	for {
		current := b.inflight.Load()
		if current >= b.maxInflight {
			return RetryPermit{}, false
		}
		if b.inflight.CompareAndSwap(current, current+1) {
			b.recordMaxObserved(current + 1)
			break
		}
	}

	for {
		current := b.credits.Load()
		if current < retryCreditUnit {
			b.inflight.Add(^uint32(0))
			return RetryPermit{}, false
		}
		if b.credits.CompareAndSwap(current, current-retryCreditUnit) {
			return RetryPermit{budget: b}, true
		}
	}
}

// Release returns the permit's in-flight slot. It panics for a nil or zero
// permit and when the same permit is released more than once.
func (p *RetryPermit) Release() {
	if p == nil || p.budget == nil {
		panic("upstream: release invalid retry permit")
	}
	if p.released.Swap(true) {
		panic("upstream: retry permit released twice")
	}
	p.budget.inflight.Add(^uint32(0))
}

func (b *retryBudget) recordMaxObserved(value uint32) {
	for {
		current := b.maxObserved.Load()
		if current >= value || b.maxObserved.CompareAndSwap(current, value) {
			return
		}
	}
}

// MaxObservedInflight returns the largest concurrent retry count observed
// since the budget was created.
func (b *retryBudget) MaxObservedInflight() uint32 {
	return b.maxObserved.Load()
}

// Inflight returns the current number of acquired retry permits.
func (b *retryBudget) Inflight() uint32 {
	return b.inflight.Load()
}

// Credits returns the current fixed-point credit balance, where 1000 credits
// permit one retry.
func (b *retryBudget) Credits() uint64 {
	return b.credits.Load()
}

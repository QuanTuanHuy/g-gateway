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

func (b *retryBudget) MaxObservedInflight() uint32 {
	return b.maxObserved.Load()
}

func (b *retryBudget) Inflight() uint32 {
	return b.inflight.Load()
}

func (b *retryBudget) Credits() uint64 {
	return b.credits.Load()
}

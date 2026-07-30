package upstream

import (
	"time"
)

const reapInterval = 250 * time.Millisecond

func (r *Registry) runReaper() {
	ticker := time.NewTicker(reapInterval)
	defer func() {
		ticker.Stop()
		close(r.reapDone)
	}()
	for {
		select {
		case <-r.reapWake:
			r.reapNow()
		case <-ticker.C:
			r.reapNow()
		case <-r.reapStop:
			r.reapNow()
			return
		}
	}
}

func (r *Registry) signalReaper() {
	// Wake-ups are coalesced so final request-path release never blocks on
	// asynchronous cleanup.
	select {
	case r.reapWake <- struct{}{}:
	default:
	}
}

func (r *Registry) reapNow() {
	r.mu.Lock()
	if len(r.retired) == 0 {
		r.mu.Unlock()
		return
	}

	survivors := make([]*PlanSet, 0, len(r.retired))
	finalized := make([]*PlanSet, 0)
	for _, set := range r.retired {
		// A retired generation is eligible only after its final ownership and
		// request reference has drained; finalized prevents duplicate cleanup.
		if set.refs.Load() != 0 || !set.finalized.CompareAndSwap(false, true) {
			survivors = append(survivors, set)
			continue
		}
		finalized = append(finalized, set)
	}
	r.retired = survivors

	cleanup := CleanupStats{}
	transports := make([]*transportRuntime, 0)
	for _, set := range finalized {
		released, closed := r.releaseRefsLocked(set.owned)
		cleanup.ReleasedEndpoints += released.ReleasedEndpoints
		cleanup.ReleasedTransports += released.ReleasedTransports
		cleanup.ClosedTransports += released.ClosedTransports
		cleanup.ReleasedHealthTrackers += released.ReleasedHealthTrackers
		cleanup.ReleasedRetryBudgets += released.ReleasedRetryBudgets
		transports = append(transports, closed...)
		// Clearing ownership makes the exactly-once reference release explicit
		// even if a future reaper pass observes the object again.
		set.owned = resourceRefs{}
	}
	cleanup.Current = r.statsLocked()
	r.mu.Unlock()

	// Close idle connections after unlocking the registry.
	closeTransports(transports)
	if len(finalized) > 0 {
		r.notifyCleaned(cleanup)
	}
}

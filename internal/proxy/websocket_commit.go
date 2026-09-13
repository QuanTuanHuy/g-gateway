package proxy

import (
	"context"
	"net"
	"sync"
	"time"
)

// webSocketCommitController arbitrates cancellation against committing a 101
// response. Once a downstream connection is pending, cancellation first sets a
// reversible write deadline; the handler that performed Flush decides which
// event happened first and owns the terminal state transition.
type webSocketCommitController struct {
	mu            sync.Mutex
	tunnelCtx     context.Context
	cancel        context.CancelFunc
	totalDeadline time.Time
	canceledAt    time.Time
	pending       net.Conn
	committed     bool
}

func newWebSocketCommitController(
	tunnelCtx context.Context,
	cancel context.CancelFunc,
	totalDeadline time.Time,
) *webSocketCommitController {
	return &webSocketCommitController{
		tunnelCtx:     tunnelCtx,
		cancel:        cancel,
		totalDeadline: totalDeadline,
	}
}

func (controller *webSocketCommitController) cancelBeforeCommit() {
	controller.cancelAt(time.Now())
}

func (controller *webSocketCommitController) cancelAt(at time.Time) {
	controller.mu.Lock()
	defer controller.mu.Unlock()
	if controller.committed {
		return
	}
	if controller.canceledAt.IsZero() {
		controller.canceledAt = at
	}
	if controller.pending != nil {
		_ = controller.pending.SetWriteDeadline(at)
		return
	}
	controller.cancel()
}

func (controller *webSocketCommitController) canContinue(requestCtx context.Context) bool {
	controller.mu.Lock()
	defer controller.mu.Unlock()
	return controller.canceledAt.IsZero() && controller.tunnelCtx.Err() == nil && requestCtx.Err() == nil &&
		(controller.totalDeadline.IsZero() || time.Now().Before(controller.totalDeadline))
}

func (controller *webSocketCommitController) setPending(connection net.Conn, requestCtx context.Context) (bool, error) {
	controller.mu.Lock()
	defer controller.mu.Unlock()
	if !controller.canceledAt.IsZero() || controller.tunnelCtx.Err() != nil || requestCtx.Err() != nil ||
		(!controller.totalDeadline.IsZero() && !time.Now().Before(controller.totalDeadline)) {
		return false, nil
	}
	if !controller.totalDeadline.IsZero() {
		if err := connection.SetWriteDeadline(controller.totalDeadline); err != nil {
			return false, err
		}
	}
	controller.pending = connection
	return true, nil
}

func (controller *webSocketCommitController) finishCommit(flushedAt time.Time) bool {
	controller.mu.Lock()
	defer controller.mu.Unlock()
	if !controller.totalDeadline.IsZero() && !flushedAt.Before(controller.totalDeadline) ||
		(!controller.canceledAt.IsZero() && !flushedAt.Before(controller.canceledAt)) {
		return false
	}
	controller.committed = true
	if controller.pending != nil {
		_ = controller.pending.SetWriteDeadline(time.Time{})
		controller.pending = nil
	}
	return true
}

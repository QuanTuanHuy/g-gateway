// Package requestctx attaches mutable gateway request state to an HTTP request
// through a private typed context key.
//
// Each attached Context belongs to one request and must not be shared across
// requests. Snapshot and runtime references remain valid only for the request
// lease that installed them.
package requestctx

import (
	"context"
	"net/http"

	"github.com/QuanTuanHuy/g-gateway/internal/model"
	"github.com/QuanTuanHuy/g-gateway/internal/upstream"
)

// RouteMeta contains the bounded route identity exposed to request processing
// and telemetry.
type RouteMeta struct {
	// ID is the matched route identifier.
	ID string
}

// ServiceMeta contains the bounded service identity resolved for a request.
type ServiceMeta struct {
	// ID is the resolved service identifier.
	ID string
}

// UpstreamMeta contains the bounded upstream identity resolved for a request.
type UpstreamMeta struct {
	// ID is the resolved upstream identifier.
	ID string
}

// ParamSpan identifies one matched route parameter by byte offsets into
// Context.Path.
type ParamSpan struct {
	// Name is the parameter name declared by the route pattern.
	Name string
	// Start is the inclusive byte offset of the value in Context.Path.
	Start int
	// End is the exclusive byte offset of the value in Context.Path.
	End int
}

// SnapshotRef exposes the revision retained by the active request lease.
type SnapshotRef interface {
	// Revision returns the retained snapshot revision.
	Revision() uint64
}

// RuntimeRoute is the request-path contract for an immutable compiled route.
// Its methods coordinate shared retry, selection, health, and response-plugin
// state while the owning snapshot lease remains held.
type RuntimeRoute interface {
	// RetryPolicy returns the effective retry policy. Its slices are borrowed
	// immutable data and must not be modified.
	RetryPolicy() model.RetryPolicy
	// ActivateUpstream lazily starts active health work for the route's
	// upstream.
	ActivateUpstream()
	// CreditPrimary records one primary request in the upstream retry budget.
	CreditPrimary()
	// AcquireRetry reserves one retry permit when budget and concurrency limits
	// allow it.
	AcquireRetry() (upstream.RetryPermit, bool)
	// SelectNext selects an eligible endpoint not present in attempted.
	SelectNext(*http.Request, *upstream.AttemptSet) (upstream.Selection, error)
	// RunResponse executes the compiled response-plugin chain.
	RunResponse(*Context, *http.Response) error
}

// Context is mutable observational and execution state owned by one HTTP
// request. It is not safe to share between requests or mutate concurrently.
type Context struct {
	// Snapshot is the snapshot retained by the request lease.
	Snapshot SnapshotRef
	// Runtime is the compiled route selected from Snapshot.
	Runtime RuntimeRoute
	// Revision records the retained snapshot revision for observation.
	Revision uint64
	// Route is metadata for the matched route, or nil before a match.
	Route *RouteMeta
	// Service is metadata for the resolved service, or nil for a direct
	// upstream route.
	Service *ServiceMeta
	// Upstream is metadata for the resolved upstream, or nil before a match.
	Upstream *UpstreamMeta
	// Selection records the most recent endpoint selection.
	Selection upstream.Selection
	// Attempt is the one-based ordinal of the current upstream attempt.
	Attempt int
	// Attempts is the total number of upstream attempts made so far.
	Attempts int
	// RetrySuppressed records a bounded reason code when an otherwise possible
	// retry is not performed.
	RetrySuppressed string
	// UpstreamOutcome records the bounded outcome code of the final observed
	// upstream attempt.
	UpstreamOutcome string
	// RequestID records the request identifier managed by the request-ID
	// plugin.
	RequestID string
	// Path is the request path against which Params offsets are valid.
	Path string
	// Params contains route-parameter spans into Path.
	Params []ParamSpan
	// Scratch contains request-owned plugin slots assigned by the compiled
	// plugin chain.
	Scratch []any
	// ResponseCode records the final downstream HTTP status; zero means no
	// status has been recorded.
	ResponseCode int
	// ResponseError records a bounded gateway error code, or an empty string
	// for a non-error response.
	ResponseError string
}

type contextKey struct{}

// Attach returns a shallow copy of request carrying a fresh Context. It
// allocates request-owned plugin scratch storage only when scratchSlots is
// positive.
func Attach(request *http.Request, scratchSlots int) (*http.Request, *Context) {
	state := &Context{}
	if scratchSlots > 0 {
		state.Scratch = make([]any, scratchSlots)
	}
	ctx := context.WithValue(request.Context(), contextKey{}, state)
	return request.WithContext(ctx), state
}

// From returns the private gateway Context attached to ctx and reports whether
// one is present.
func From(ctx context.Context) (*Context, bool) {
	state, ok := ctx.Value(contextKey{}).(*Context)
	return state, ok
}

// Middleware attaches one fresh gateway Context before invoking next.
func Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		request, _ = Attach(request, 0)
		next.ServeHTTP(response, request)
	})
}

// AllocateScratch replaces the plugin scratch storage with the requested
// number of fresh entries when slots is positive. Zero or negative values
// leave the current storage unchanged.
func (c *Context) AllocateScratch(slots int) {
	if slots > 0 {
		c.Scratch = make([]any, slots)
	}
}

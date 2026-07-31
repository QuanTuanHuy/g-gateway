package runtime

import (
	"net/http"
	"time"

	"github.com/QuanTuanHuy/g-gateway/internal/model"
	"github.com/QuanTuanHuy/g-gateway/internal/plugin"
	"github.com/QuanTuanHuy/g-gateway/internal/requestctx"
	"github.com/QuanTuanHuy/g-gateway/internal/router"
	"github.com/QuanTuanHuy/g-gateway/internal/upstream"
)

// Stats contains bounded metadata for one compiled snapshot.
type Stats struct {
	// Revision is the published configuration revision.
	Revision uint64
	// RouteCount is the number of compiled routes.
	RouteCount int
	// ServiceCount is the number of canonical services.
	ServiceCount int
	// UpstreamCount is the number of canonical upstreams.
	UpstreamCount int
	// PluginCount is the total enabled plugin attachments across compiled
	// routes.
	PluginCount int
	// BuildDuration is the elapsed time spent preparing and compiling the
	// snapshot.
	BuildDuration time.Duration
}

// CompiledRoute is an immutable request-path binding of metadata, plugins,
// effective retry policy, and one upstream plan. Returned metadata and policy
// slices are borrowed and must not be mutated or retained beyond the owning
// snapshot lease.
type CompiledRoute struct {
	meta         *requestctx.RouteMeta
	service      *requestctx.ServiceMeta
	upstreamMeta *requestctx.UpstreamMeta
	plan         *upstream.Plan
	plugins      *plugin.Chain
	retry        model.RetryPolicy
}

// Snapshot is one immutable compiled configuration revision. Its routes and
// upstream resources remain valid while the Manager lease that exposed it is
// held.
type Snapshot struct {
	revision uint64
	router   *router.Router
	routes   []CompiledRoute
	plans    *upstream.PlanSet
	stats    Stats
}

// Match describes a compiled route match, method-only mismatch, or no match.
// Route and Params are borrowed from or refer to the leased Snapshot and
// request path.
type Match struct {
	// Found reports that Route identifies the winning route.
	Found bool
	// MethodNotAllowed reports a method-only mismatch.
	MethodNotAllowed bool
	// Route is the winning compiled route, or nil when Found is false.
	Route *CompiledRoute
	// Params contains byte spans into the matched request URL path.
	Params []requestctx.ParamSpan
	// Allow is the sorted, deduplicated method list for a method-only mismatch.
	Allow []string
}

// Revision returns the snapshot revision, or zero for a nil snapshot.
func (s *Snapshot) Revision() uint64 {
	if s == nil {
		return 0
	}
	return s.revision
}

// Match routes request without configuration lookup and may return
// router.ErrInvalidQuery for malformed query escaping.
func (s *Snapshot) Match(request *http.Request) (Match, error) {
	result, err := s.router.Match(request)
	if err != nil {
		return Match{}, err
	}
	match := Match{
		Found:            result.Found,
		MethodNotAllowed: result.MethodNotAllowed,
		Params:           result.Params,
		Allow:            result.Allow,
	}
	if result.Found {
		match.Route = &s.routes[result.RouteIndex]
	}
	return match, nil
}

// Meta returns borrowed route metadata.
func (r *CompiledRoute) Meta() *requestctx.RouteMeta {
	return r.meta
}

// ServiceMeta returns borrowed service metadata, or nil for a direct upstream
// route.
func (r *CompiledRoute) ServiceMeta() *requestctx.ServiceMeta {
	return r.service
}

// UpstreamMeta returns borrowed metadata for the resolved upstream.
func (r *CompiledRoute) UpstreamMeta() *requestctx.UpstreamMeta {
	return r.upstreamMeta
}

// ScratchSlots returns the number of request-owned plugin scratch entries
// required by the route.
func (r *CompiledRoute) ScratchSlots() int {
	return r.plugins.ScratchSlots()
}

// RunRequest executes the route's compiled request-plugin chain.
func (r *CompiledRoute) RunRequest(state *requestctx.Context, request *http.Request) plugin.RequestResult {
	return r.plugins.RunRequest(state, request)
}

// RunResponse executes the route's compiled response-plugin chain.
func (r *CompiledRoute) RunResponse(state *requestctx.Context, response *http.Response) error {
	return r.plugins.RunResponse(state, response)
}

// RetryPolicy returns the effective policy for the route. Its slices are
// borrowed immutable data and must not be modified.
func (r *CompiledRoute) RetryPolicy() model.RetryPolicy {
	return r.retry
}

// ActivateUpstream lazily activates health scheduling for the route's
// upstream.
func (r *CompiledRoute) ActivateUpstream() {
	r.plan.ActivateHealth()
}

// CreditPrimary records one primary request in the upstream retry budget.
func (r *CompiledRoute) CreditPrimary() {
	r.plan.CreditPrimary()
}

// AcquireRetry reserves one retry permit when the shared upstream budget
// allows it.
func (r *CompiledRoute) AcquireRetry() (upstream.RetryPermit, bool) {
	return r.plan.AcquireRetry()
}

// SelectNext chooses a healthy or unknown endpoint not present in attempted.
func (r *CompiledRoute) SelectNext(request *http.Request, attempted *upstream.AttemptSet) (upstream.Selection, error) {
	return r.plan.SelectNext(request, attempted)
}

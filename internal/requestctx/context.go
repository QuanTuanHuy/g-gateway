package requestctx

import (
	"context"
	"net/http"

	"github.com/QuanTuanHuy/g-gateway/internal/model"
	"github.com/QuanTuanHuy/g-gateway/internal/upstream"
)

type RouteMeta struct {
	ID string
}

type ServiceMeta struct {
	ID string
}

type UpstreamMeta struct {
	ID string
}

type ParamSpan struct {
	Name  string
	Start int
	End   int
}

type SnapshotRef interface {
	Revision() uint64
}

type RuntimeRoute interface {
	RetryPolicy() model.RetryPolicy
	ActivateUpstream()
	CreditPrimary()
	AcquireRetry() (upstream.RetryPermit, bool)
	SelectNext(*http.Request, *upstream.AttemptSet) (upstream.Selection, error)
	RunResponse(*Context, *http.Response) error
}

type Context struct {
	Snapshot        SnapshotRef
	Runtime         RuntimeRoute
	Revision        uint64
	Route           *RouteMeta
	Service         *ServiceMeta
	Upstream        *UpstreamMeta
	Selection       upstream.Selection
	Attempt         int
	Attempts        int
	RetrySuppressed string
	UpstreamOutcome string
	RequestID       string
	Path            string
	Params          []ParamSpan
	Scratch         []any
	ResponseCode    int
	ResponseError   string
}

type contextKey struct{}

func Attach(request *http.Request, scratchSlots int) (*http.Request, *Context) {
	state := &Context{}
	if scratchSlots > 0 {
		state.Scratch = make([]any, scratchSlots)
	}
	ctx := context.WithValue(request.Context(), contextKey{}, state)
	return request.WithContext(ctx), state
}

func From(ctx context.Context) (*Context, bool) {
	state, ok := ctx.Value(contextKey{}).(*Context)
	return state, ok
}

func Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		request, _ = Attach(request, 0)
		next.ServeHTTP(response, request)
	})
}

func (c *Context) AllocateScratch(slots int) {
	if slots > 0 {
		c.Scratch = make([]any, slots)
	}
}

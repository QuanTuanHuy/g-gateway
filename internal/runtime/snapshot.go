package runtime

import (
	"net/http"
	"net/url"
	"time"

	"github.com/QuanTuanHuy/g-gateway/internal/plugin"
	"github.com/QuanTuanHuy/g-gateway/internal/requestctx"
	"github.com/QuanTuanHuy/g-gateway/internal/router"
	"github.com/QuanTuanHuy/g-gateway/internal/upstream"
)

type Stats struct {
	Revision      uint64
	RouteCount    int
	ServiceCount  int
	UpstreamCount int
	PluginCount   int
	BuildDuration time.Duration
}

type CompiledRoute struct {
	meta         *requestctx.RouteMeta
	service      *requestctx.ServiceMeta
	upstreamMeta *requestctx.UpstreamMeta
	upstream     *upstream.Runtime
	plugins      *plugin.Chain
}

type Snapshot struct {
	revision uint64
	router   *router.Router
	routes   []CompiledRoute
	stats    Stats
}

type Match struct {
	Found            bool
	MethodNotAllowed bool
	Route            *CompiledRoute
	Params           []requestctx.ParamSpan
	Allow            []string
}

func (s *Snapshot) Revision() uint64 {
	if s == nil {
		return 0
	}
	return s.revision
}

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

func (r *CompiledRoute) Meta() *requestctx.RouteMeta {
	return r.meta
}

func (r *CompiledRoute) ServiceMeta() *requestctx.ServiceMeta {
	return r.service
}

func (r *CompiledRoute) UpstreamMeta() *requestctx.UpstreamMeta {
	return r.upstreamMeta
}

func (r *CompiledRoute) ScratchSlots() int {
	return r.plugins.ScratchSlots()
}

func (r *CompiledRoute) RunRequest(state *requestctx.Context, request *http.Request) plugin.RequestResult {
	return r.plugins.RunRequest(state, request)
}

func (r *CompiledRoute) RunResponse(state *requestctx.Context, response *http.Response) error {
	return r.plugins.RunResponse(state, response)
}

func (r *CompiledRoute) Target() *url.URL {
	return r.upstream.Target()
}

func (r *CompiledRoute) RoundTrip(request *http.Request) (*http.Response, error) {
	return r.upstream.RoundTripper().RoundTrip(request)
}

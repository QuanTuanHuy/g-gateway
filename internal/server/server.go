package server

import (
	"github.com/valyala/fasthttp"
	"github.com/QuanTuanHuy/g-gateway/internal/core"
	"github.com/QuanTuanHuy/g-gateway/internal/plugin"
	"github.com/QuanTuanHuy/g-gateway/internal/proxy"
)

type Server struct {
	gw      *core.Gateway
	proxy   *proxy.Proxy
	plugins []plugin.Plugin
}

func NewServer(gw *core.Gateway, px *proxy.Proxy, plugins []plugin.Plugin) *Server {
	return &Server{
		gw:      gw,
		proxy:   px,
		plugins: plugins,
	}
}

func (s *Server) HandleRequest(ctx *fasthttp.RequestCtx) {
	path := string(ctx.Path())
	rt := s.gw.GetRouteTable()

	route, found := rt.Router.Match(path)
	if !found {
		ctx.SetStatusCode(fasthttp.StatusNotFound)
		ctx.SetBodyString("404 Route Not Found")
		return
	}

	// 1. PreHandle Plugin Pipeline
	for _, p := range s.plugins {
		cont, err := p.PreHandle(ctx, route)
		if err != nil {
			ctx.SetStatusCode(fasthttp.StatusInternalServerError)
			return
		}
		if !cont {
			return
		}
	}

	// 2. Upstream Proxy Stage
	if err := s.proxy.Forward(ctx, route.Upstream); err != nil {
		ctx.SetStatusCode(fasthttp.StatusBadGateway)
		ctx.SetBodyString("502 Bad Gateway")
		return
	}

	// 3. PostHandle Plugin Pipeline
	for _, p := range s.plugins {
		_ = p.PostHandle(ctx, route)
	}
}

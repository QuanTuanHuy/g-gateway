package server_test

import (
	"net"
	"testing"
	"github.com/stretchr/testify/assert"
	"github.com/valyala/fasthttp"
	"github.com/valyala/fasthttp/fasthttputil"
	"github.com/QuanTuanHuy/g-gateway/internal/core"
	"github.com/QuanTuanHuy/g-gateway/internal/plugin"
	"github.com/QuanTuanHuy/g-gateway/internal/proxy"
	"github.com/QuanTuanHuy/g-gateway/internal/server"
)

type mockHeaderPlugin struct{}

func (m *mockHeaderPlugin) Name() string { return "mock-header" }
func (m *mockHeaderPlugin) PreHandle(ctx *fasthttp.RequestCtx, route *plugin.RouteInfo) (bool, error) {
	ctx.Request.Header.Set("X-Gateway-Auth", "Passed")
	return true, nil
}
func (m *mockHeaderPlugin) PostHandle(ctx *fasthttp.RequestCtx, route *plugin.RouteInfo) error {
	ctx.Response.Header.Set("X-Gateway-Latency", "0ms")
	return nil
}

func TestServerPipelineRequestFlow(t *testing.T) {
	ln := fasthttputil.NewInmemoryListener()
	defer ln.Close()

	go func() {
		_ = fasthttp.Serve(ln, func(ctx *fasthttp.RequestCtx) {
			assert.Equal(t, "Passed", string(ctx.Request.Header.Peek("X-Gateway-Auth")))
			ctx.SetStatusCode(fasthttp.StatusOK)
			ctx.SetBodyString("Backend Handled")
		})
	}()

	client := &fasthttp.Client{
		Dial: func(addr string) (net.Conn, error) {
			return ln.Dial()
		},
	}

	gw := core.NewGateway()
	rt := core.NewRouteTable(1)
	_ = rt.AddRoute(&plugin.RouteInfo{ID: "r1", URI: "/api/test", Upstream: "http://backend"})
	gw.SwapRouteTable(rt)

	px := proxy.NewProxyWithClient(client)
	plugins := []plugin.Plugin{&mockHeaderPlugin{}}

	srv := server.NewServer(gw, px, plugins)

	// Test 1: Match Route
	ctx := &fasthttp.RequestCtx{}
	ctx.Request.SetRequestURI("/api/test")

	srv.HandleRequest(ctx)

	assert.Equal(t, fasthttp.StatusOK, ctx.Response.StatusCode())
	assert.Equal(t, "Backend Handled", string(ctx.Response.Body()))
	assert.Equal(t, "0ms", string(ctx.Response.Header.Peek("X-Gateway-Latency")))

	// Test 2: 404 Route Not Found
	ctx404 := &fasthttp.RequestCtx{}
	ctx404.Request.SetRequestURI("/unknown")

	srv.HandleRequest(ctx404)
	assert.Equal(t, fasthttp.StatusNotFound, ctx404.Response.StatusCode())
}

package proxy_test

import (
	"net"
	"testing"
	"github.com/stretchr/testify/assert"
	"github.com/valyala/fasthttp"
	"github.com/valyala/fasthttp/fasthttputil"
	"github.com/QuanTuanHuy/g-gateway/internal/proxy"
)

func TestReverseProxyForwarding(t *testing.T) {
	ln := fasthttputil.NewInmemoryListener()
	defer ln.Close()

	// Mock backend upstream server
	go func() {
		_ = fasthttp.Serve(ln, func(ctx *fasthttp.RequestCtx) {
			ctx.SetStatusCode(fasthttp.StatusOK)
			ctx.SetBodyString("Hello from Backend Upstream!")
		})
	}()

	client := &fasthttp.Client{
		Dial: func(addr string) (net.Conn, error) {
			return ln.Dial()
		},
	}

	px := proxy.NewProxyWithClient(client)

	ctx := &fasthttp.RequestCtx{}
	ctx.Request.SetRequestURI("http://dummy-upstream/test")

	err := px.Forward(ctx, "http://dummy-upstream")
	assert.NoError(t, err)
	assert.Equal(t, fasthttp.StatusOK, ctx.Response.StatusCode())
	assert.Equal(t, "Hello from Backend Upstream!", string(ctx.Response.Body()))
}

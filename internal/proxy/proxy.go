package proxy

import (
	"strings"
	"github.com/valyala/fasthttp"
)

type Proxy struct {
	client *fasthttp.Client
}

func NewProxy() *Proxy {
	return &Proxy{
		client: &fasthttp.Client{},
	}
}

func NewProxyWithClient(client *fasthttp.Client) *Proxy {
	return &Proxy{
		client: client,
	}
}

func (p *Proxy) Forward(ctx *fasthttp.RequestCtx, upstreamTarget string) error {
	req := &ctx.Request
	resp := &ctx.Response

	targetURL := strings.TrimRight(upstreamTarget, "/") + string(ctx.Path())
	if len(ctx.QueryArgs().QueryString()) > 0 {
		targetURL += "?" + string(ctx.QueryArgs().QueryString())
	}

	req.SetRequestURI(targetURL)

	return p.client.Do(req, resp)
}

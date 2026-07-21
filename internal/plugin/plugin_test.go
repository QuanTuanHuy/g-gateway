package plugin_test

import (
	"testing"
	"github.com/stretchr/testify/assert"
	"github.com/valyala/fasthttp"
	"github.com/QuanTuanHuy/g-gateway/internal/plugin"
)

type mockHeaderPlugin struct {
	headerKey   string
	headerValue string
}

func (m *mockHeaderPlugin) Name() string { return "mock-header" }
func (m *mockHeaderPlugin) PreHandle(ctx *fasthttp.RequestCtx, route *plugin.RouteInfo) (bool, error) {
	ctx.Request.Header.Set(m.headerKey, m.headerValue)
	return true, nil
}
func (m *mockHeaderPlugin) PostHandle(ctx *fasthttp.RequestCtx, route *plugin.RouteInfo) error {
	ctx.Response.Header.Set("X-Processed-By", "G-Gateway")
	return nil
}

func TestPluginPipeline(t *testing.T) {
	p := &mockHeaderPlugin{headerKey: "X-Test-Key", headerValue: "TestVal"}
	ctx := &fasthttp.RequestCtx{}
	route := &plugin.RouteInfo{ID: "r1", URI: "/test"}

	cont, err := p.PreHandle(ctx, route)
	assert.NoError(t, err)
	assert.True(t, cont)
	assert.Equal(t, "TestVal", string(ctx.Request.Header.Peek("X-Test-Key")))

	err = p.PostHandle(ctx, route)
	assert.NoError(t, err)
	assert.Equal(t, "G-Gateway", string(ctx.Response.Header.Peek("X-Processed-By")))
}

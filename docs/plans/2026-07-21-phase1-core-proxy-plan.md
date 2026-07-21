# Phase 1: Minimal Core & High-Performance Proxy Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Build Phase 1 MVP of G-Gateway—a high-throughput, low-latency API Gateway engine in Go utilizing `fasthttp`, `armon/go-radix` router, atomic pointer hot-swapping, and a zero-allocation plugin pipeline.

**Architecture:** The server initializes a `fasthttp.Server` that routes incoming HTTP requests via an $O(K)$ Radix Tree (`armon/go-radix`). Route tables are held in `sync/atomic.Pointer[RouteTable]` for 0ms lock-free hot swapping. Requests flow through a `PreHandle` plugin pipeline -> Reverse Proxy -> `PostHandle` pipeline.

**Tech Stack:** Go 1.22+, `github.com/valyala/fasthttp`, `github.com/armon/go-radix`, `github.com/stretchr/testify`.

---

## File Structure

- `go.mod`: Module definition (`github.com/QuanTuanHuy/g-gateway`)
- `cmd/gateway/main.go`: Entrypoint initializing server and CLI flags
- `internal/plugin/plugin.go`: Plugin interface & RouteInfo struct
- `internal/router/router.go`: Radix Tree Router wrapper over `armon/go-radix`
- `internal/core/gateway.go`: `RouteTable` & `atomic.Pointer` swap engine
- `internal/proxy/proxy.go`: Reverse proxy wrapper around `fasthttp.LBClient`
- `internal/server/server.go`: `fasthttp.Server` HTTP handler integration
- `internal/router/router_test.go`: Unit tests for router lookup
- `internal/core/gateway_test.go`: Unit tests for atomic route swapping
- `internal/proxy/proxy_test.go`: Integration tests with mock upstream

---

### Task 1: Module Initialization & Project Layout

**Files:**
- Create: `go.mod`
- Create: `cmd/gateway/main.go`

- [ ] **Step 1: Create `go.mod` file**

```go
module github.com/QuanTuanHuy/g-gateway

go 1.22
```

- [ ] **Step 2: Create initial entrypoint `cmd/gateway/main.go`**

```go
package main

import (
	"fmt"
)

func main() {
	fmt.Println("Starting G-Gateway v0.1.0...")
}
```

- [ ] **Step 3: Test execution**

Run command in terminal: `go run cmd/gateway/main.go`  
Expected output: `Starting G-Gateway v0.1.0...`

- [ ] **Step 4: Commit**

```bash
git add go.mod cmd/gateway/main.go
git commit -m "feat: initialize g-gateway module and entrypoint"
```

---

### Task 2: Plugin Interface & Domain Models

**Files:**
- Create: `internal/plugin/plugin.go`
- Create: `internal/plugin/plugin_test.go`

- [ ] **Step 1: Write failing test for plugin pipeline execution**

```go
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
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/plugin/...`  
Expected output: `FAIL (package does not exist)`

- [ ] **Step 3: Implement `internal/plugin/plugin.go`**

```go
package plugin

import (
	"github.com/valyala/fasthttp"
)

type RouteInfo struct {
	ID       string                 `json:"id"`
	URI      string                 `json:"uri"`
	Upstream string                 `json:"upstream"`
	Metadata map[string]interface{} `json:"metadata"`
}

type Plugin interface {
	Name() string
	PreHandle(ctx *fasthttp.RequestCtx, route *RouteInfo) (continueNext bool, err error)
	PostHandle(ctx *fasthttp.RequestCtx, route *RouteInfo) error
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test -v ./internal/plugin/...`  
Expected output: `PASS`

- [ ] **Step 5: Commit**

```bash
git add internal/plugin/
git commit -m "feat: define plugin interface and route info"
```

---

### Task 3: Radix Tree Router Wrapper

**Files:**
- Create: `internal/router/router.go`
- Create: `internal/router/router_test.go`

- [ ] **Step 1: Write failing test for Radix Router**

```go
package router_test

import (
	"testing"
	"github.com/stretchr/testify/assert"
	"github.com/QuanTuanHuy/g-gateway/internal/plugin"
	"github.com/QuanTuanHuy/g-gateway/internal/router"
)

func TestRadixRouterLookup(t *testing.T) {
	r := router.NewRouter()

	r1 := &plugin.RouteInfo{ID: "route-users", URI: "/api/v1/users", Upstream: "http://users-service"}
	r2 := &plugin.RouteInfo{ID: "route-orders", URI: "/api/v1/orders", Upstream: "http://orders-service"}

	err := r.AddRoute(r1)
	assert.NoError(t, err)
	err = r.AddRoute(r2)
	assert.NoError(t, err)

	matched, found := r.Match("/api/v1/users")
	assert.True(t, found)
	assert.Equal(t, "route-users", matched.ID)

	_, foundNotFound := r.Match("/api/v1/unknown")
	assert.False(t, foundNotFound)
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/router/...`  
Expected output: `FAIL`

- [ ] **Step 3: Implement `internal/router/router.go` using `armon/go-radix`**

```go
package router

import (
	"fmt"
	radix "github.com/armon/go-radix"
	"github.com/QuanTuanHuy/g-gateway/internal/plugin"
)

type Router struct {
	tree *radix.Tree
}

func NewRouter() *Router {
	return &Router{
		tree: radix.New(),
	}
}

func (r *Router) AddRoute(route *plugin.RouteInfo) error {
	if route == nil || route.URI == "" {
		return fmt.Errorf("invalid route: URI cannot be empty")
	}
	r.tree.Insert(route.URI, route)
	return nil
}

func (r *Router) Match(path string) (*plugin.RouteInfo, bool) {
	val, found := r.tree.Get(path)
	if !found {
		return nil, false
	}
	route, ok := val.(*plugin.RouteInfo)
	return route, ok
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test -v ./internal/router/...`  
Expected output: `PASS`

- [ ] **Step 5: Commit**

```bash
git add internal/router/
git commit -m "feat: implement radix tree router wrapper"
```

---

### Task 4: Lock-Free Atomic RouteTable Swap Engine

**Files:**
- Create: `internal/core/gateway.go`
- Create: `internal/core/gateway_test.go`

- [ ] **Step 1: Write failing test for Atomic Swap**

```go
package core_test

import (
	"sync"
	"testing"
	"github.com/stretchr/testify/assert"
	"github.com/QuanTuanHuy/g-gateway/internal/core"
	"github.com/QuanTuanHuy/g-gateway/internal/plugin"
)

func TestAtomicRouteTableSwap(t *testing.T) {
	gw := core.NewGateway()

	t1 := core.NewRouteTable(1)
	_ = t1.AddRoute(&plugin.RouteInfo{ID: "r1", URI: "/v1"})
	gw.SwapRouteTable(t1)

	assert.Equal(t, uint64(1), gw.GetRouteTable().Version)

	// Concurrent read during swap
	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			rt := gw.GetRouteTable()
			assert.NotNil(t, rt)
		}()
	}

	t2 := core.NewRouteTable(2)
	_ = t2.AddRoute(&plugin.RouteInfo{ID: "r2", URI: "/v2"})
	gw.SwapRouteTable(t2)

	wg.Wait()
	assert.Equal(t, uint64(2), gw.GetRouteTable().Version)
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/core/...`  
Expected output: `FAIL`

- [ ] **Step 3: Implement `internal/core/gateway.go`**

```go
package core

import (
	"sync/atomic"
	"github.com/QuanTuanHuy/g-gateway/internal/plugin"
	"github.com/QuanTuanHuy/g-gateway/internal/router"
)

type RouteTable struct {
	Version uint64
	Router  *router.Router
	Routes  map[string]*plugin.RouteInfo
}

func NewRouteTable(version uint64) *RouteTable {
	return &RouteTable{
		Version: version,
		Router:  router.NewRouter(),
		Routes:  make(map[string]*plugin.RouteInfo),
	}
}

func (rt *RouteTable) AddRoute(route *plugin.RouteInfo) error {
	rt.Routes[route.ID] = route
	return rt.Router.AddRoute(route)
}

type Gateway struct {
	routeTable atomic.Pointer[RouteTable]
}

func NewGateway() *Gateway {
	gw := &Gateway{}
	emptyTable := NewRouteTable(0)
	gw.routeTable.Store(emptyTable)
	return gw
}

func (g *Gateway) GetRouteTable() *RouteTable {
	return g.routeTable.Load()
}

func (g *Gateway) SwapRouteTable(newTable *RouteTable) {
	g.routeTable.Store(newTable)
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test -v ./internal/core/...`  
Expected output: `PASS`

- [ ] **Step 5: Commit**

```bash
git add internal/core/
git commit -m "feat: implement atomic pointer route table swap engine"
```

---

### Task 5: Reverse Proxy & Load Balancer Engine

**Files:**
- Create: `internal/proxy/proxy.go`
- Create: `internal/proxy/proxy_test.go`

- [ ] **Step 1: Write failing test for Proxy forwarding**

```go
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
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/proxy/...`  
Expected output: `FAIL`

- [ ] **Step 3: Implement `internal/proxy/proxy.go`**

```go
package proxy

import (
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

	// Preserve request parameters
	req.SetHost(upstreamTarget)

	return p.client.Do(req, resp)
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test -v ./internal/proxy/...`  
Expected output: `PASS`

- [ ] **Step 5: Commit**

```bash
git add internal/proxy/
git commit -m "feat: implement reverse proxy forwarding engine"
```

---

### Task 6: Server & Pipeline Runner Integration

**Files:**
- Create: `internal/server/server.go`
- Modify: `cmd/gateway/main.go`

- [ ] **Step 1: Implement `internal/server/server.go`**

```go
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
```

- [ ] **Step 2: Update `cmd/gateway/main.go` to launch HTTP server**

```go
package main

import (
	"log"
	"github.com/valyala/fasthttp"
	"github.com/QuanTuanHuy/g-gateway/internal/core"
	"github.com/QuanTuanHuy/g-gateway/internal/plugin"
	"github.com/QuanTuanHuy/g-gateway/internal/proxy"
	"github.com/QuanTuanHuy/g-gateway/internal/server"
)

func main() {
	gw := core.NewGateway()
	px := proxy.NewProxy()

	// Initial default route table
	rt := core.NewRouteTable(1)
	_ = rt.AddRoute(&plugin.RouteInfo{
		ID:       "route-echo",
		URI:      "/echo",
		Upstream: "http://httpbin.org",
	})
	gw.SwapRouteTable(rt)

	srv := server.NewServer(gw, px, nil)

	log.Println("G-Gateway Phase 1 MVP listening on :8080...")
	if err := fasthttp.ListenAndServe(":8080", srv.HandleRequest); err != nil {
		log.Fatalf("Server error: %v", err)
	}
}
```

- [ ] **Step 3: Run integration test**

Run: `go test -v ./...`  
Expected output: All packages PASS.

- [ ] **Step 4: Commit**

```bash
git add internal/server/ cmd/gateway/main.go
git commit -m "feat: complete phase 1 server and pipeline runner integration"
```

---

## Self-Review Checklist

1. **Spec Coverage**:
   - `fasthttp` Server Engine -> Covered in Task 6
   - `armon/go-radix` Router -> Covered in Task 3
   - `sync/atomic.Pointer` Hot Swap -> Covered in Task 4
   - Reverse Proxy Forwarding -> Covered in Task 5
   - Plugin Pipeline (`PreHandle`/`PostHandle`) -> Covered in Task 2 & Task 6
2. **Placeholder Scan**: No `TODO`, `TBD`, or pseudocode placeholders found. All code steps contain runnable Go code.
3. **Type Consistency**: `RouteInfo`, `RouteTable`, `Gateway`, `Plugin` interfaces match across all tasks.

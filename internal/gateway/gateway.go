package gateway

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"github.com/QuanTuanHuy/g-gateway/internal/config"
	"github.com/QuanTuanHuy/g-gateway/internal/model"
	"github.com/QuanTuanHuy/g-gateway/internal/plugin"
	"github.com/QuanTuanHuy/g-gateway/internal/proxy"
	"github.com/QuanTuanHuy/g-gateway/internal/requestctx"
	gatewayruntime "github.com/QuanTuanHuy/g-gateway/internal/runtime"
	"github.com/QuanTuanHuy/g-gateway/internal/telemetry"
	"github.com/QuanTuanHuy/g-gateway/internal/upstream"
)

// Addresses contains the actual listener addresses bound by Start.
type Addresses struct {
	// HTTP is the cleartext traffic listener address.
	HTTP string
	// HTTPS is the TLS traffic listener address.
	HTTPS string
	// Admin is the private telemetry listener address.
	Admin string
}

// Gateway owns the HTTP, HTTPS, and admin servers together with telemetry and
// immutable runtime resources. A Gateway must not be copied after first use.
// After a successful Start, Apply, Wait, and Shutdown may be used concurrently;
// Start must not race with Shutdown.
type Gateway struct {
	httpServer  *http.Server
	httpsServer *http.Server
	adminServer *http.Server
	tlsConfig   *tls.Config

	httpListener  net.Listener
	httpsListener net.Listener
	adminListener net.Listener

	telemetry *telemetry.Telemetry
	lifecycle *lifecycleObserver
	manager   *gatewayruntime.Manager
	logger    *slog.Logger
	closing   atomic.Bool

	startMu         sync.Mutex
	started         bool
	serveWG         sync.WaitGroup
	serveDone       chan struct{}
	serveErrors     chan error
	trafficRequests sync.WaitGroup

	shutdownOnce sync.Once
	shutdownErr  error
}

// New validates its non-nil logger, loads the configured TLS key pair,
// constructs all runtime components, and activates resources as revision 1.
// It returns no partially usable Gateway and cleans up owned components on any
// construction failure.
func New(bootstrap config.BootstrapConfig, resources model.ResourceSet, logger *slog.Logger) (*Gateway, error) {
	if logger == nil {
		return nil, fmt.Errorf("logger must not be nil")
	}
	telemetryRuntime, err := telemetry.New(bootstrap.Telemetry.RequestMetricsEnabled, bootstrap.Telemetry.ProfilingEnabled)
	if err != nil {
		return nil, fmt.Errorf("construct telemetry: %w", err)
	}
	certificate, err := tls.LoadX509KeyPair(bootstrap.HTTPS.CertificateFile, bootstrap.HTTPS.PrivateKeyFile)
	if err != nil {
		return nil, fmt.Errorf("load TLS key pair: %w", err)
	}
	pluginRegistry, err := plugin.NewBuiltinRegistry()
	if err != nil {
		return nil, fmt.Errorf("construct plugin registry: %w", err)
	}
	maxRetiredSnapshots := bootstrap.Runtime.MaxRetiredSnapshots
	if maxRetiredSnapshots == 0 {
		maxRetiredSnapshots = config.DefaultMaxRetiredSnapshots
	}
	healthWorkers := bootstrap.Runtime.Health.Workers
	if healthWorkers == 0 {
		healthWorkers = config.DefaultHealthWorkers
	}
	healthQueueCapacity := bootstrap.Runtime.Health.ReadyQueueCapacity
	if healthQueueCapacity == 0 {
		healthQueueCapacity = config.DefaultHealthQueueCapacity
	}
	lifecycle := newLifecycleObserver(telemetryRuntime, logger)
	upstreamRegistry, err := upstream.NewRegistry(upstream.RegistryOptions{
		MaxRetiredSnapshots: maxRetiredSnapshots,
		HealthWorkers:       healthWorkers,
		HealthQueueCapacity: healthQueueCapacity,
		Observer:            lifecycle,
	})
	if err != nil {
		return nil, fmt.Errorf("construct upstream registry: %w", err)
	}
	closeRegistry := func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = upstreamRegistry.Close(ctx)
	}
	if err := telemetryRuntime.RegisterResilienceProvider(upstreamRegistry); err != nil {
		closeRegistry()
		return nil, fmt.Errorf("register resilience telemetry: %w", err)
	}
	builder, err := gatewayruntime.NewBuilder(pluginRegistry)
	if err != nil {
		closeRegistry()
		return nil, fmt.Errorf("construct runtime builder: %w", err)
	}
	manager := gatewayruntime.NewManager(builder, upstreamRegistry, lifecycle)
	closeManager := func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = manager.Close(ctx)
		lifecycle.ShutdownCleanup(manager.UpstreamStats())
	}
	if err := manager.Apply(1, resources); err != nil {
		closeManager()
		return nil, fmt.Errorf("activate initial runtime snapshot: %w", err)
	}
	proxyHandler, err := proxy.NewRuntime(proxy.RuntimeOptions{
		Snapshots:           manager,
		MaxRequestBodyBytes: bootstrap.Server.MaxRequestBodyBytes,
		Logger:              logger,
	})
	if err != nil {
		closeManager()
		return nil, fmt.Errorf("construct proxy handler: %w", err)
	}

	httpProtocols := new(http.Protocols)
	httpProtocols.SetHTTP1(true)
	tlsProtocols := new(http.Protocols)
	tlsProtocols.SetHTTP1(true)
	tlsProtocols.SetHTTP2(true)
	adminProtocols := new(http.Protocols)
	adminProtocols.SetHTTP1(true)

	tlsConfig := &tls.Config{
		Certificates: []tls.Certificate{certificate},
		MinVersion:   tls.VersionTLS12,
		NextProtos:   []string{"h2", "http/1.1"},
	}
	gateway := &Gateway{
		tlsConfig:   tlsConfig,
		telemetry:   telemetryRuntime,
		lifecycle:   lifecycle,
		manager:     manager,
		logger:      logger,
		serveDone:   make(chan struct{}),
		serveErrors: make(chan error, 3),
	}
	trafficHandler := recoverPanics(
		gateway.trackTraffic(requestctx.Middleware(telemetryRuntime.Wrap(proxyHandler))),
		logger,
	)
	gateway.httpServer = &http.Server{
		Addr:              bootstrap.HTTP.Address,
		Handler:           trafficHandler,
		ReadHeaderTimeout: bootstrap.Server.ReadHeaderTimeout,
		IdleTimeout:       bootstrap.Server.IdleTimeout,
		MaxHeaderBytes:    bootstrap.Server.MaxHeaderBytes,
		Protocols:         httpProtocols,
	}
	gateway.httpsServer = &http.Server{
		Addr:              bootstrap.HTTPS.Address,
		Handler:           trafficHandler,
		ReadHeaderTimeout: bootstrap.Server.ReadHeaderTimeout,
		IdleTimeout:       bootstrap.Server.IdleTimeout,
		MaxHeaderBytes:    bootstrap.Server.MaxHeaderBytes,
		TLSConfig:         tlsConfig,
		Protocols:         tlsProtocols,
	}
	gateway.adminServer = &http.Server{
		Addr:              bootstrap.Admin.Address,
		Handler:           telemetryRuntime.AdminHandler(),
		ReadHeaderTimeout: bootstrap.Server.ReadHeaderTimeout,
		IdleTimeout:       bootstrap.Server.IdleTimeout,
		MaxHeaderBytes:    bootstrap.Server.MaxHeaderBytes,
		Protocols:         adminProtocols,
	}
	return gateway, nil
}

func (g *Gateway) trackTraffic(next http.Handler) http.Handler {
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		g.trafficRequests.Add(1)
		defer g.trafficRequests.Done()
		next.ServeHTTP(writer, request)
	})
}

// Apply publishes a strictly newer immutable resource revision. It rejects
// updates once Shutdown begins, and a rejected update leaves the last-known-
// good snapshot active.
func (g *Gateway) Apply(revision uint64, resources model.ResourceSet) error {
	if g.closing.Load() {
		return fmt.Errorf("GATEWAY_SHUTTING_DOWN: runtime updates are disabled")
	}
	return g.manager.Apply(revision, resources)
}

// Start binds admin, HTTP, then HTTPS listeners and starts serving only after
// all three binds succeed. It may succeed once, marks readiness only after
// serving goroutines start, and returns the actual bound addresses. A failed
// bind leaves readiness false and closes listeners opened by that call.
func (g *Gateway) Start() (Addresses, error) {
	g.startMu.Lock()
	defer g.startMu.Unlock()
	if g.started {
		return Addresses{}, fmt.Errorf("gateway already started")
	}

	adminListener, err := net.Listen("tcp", g.adminServer.Addr)
	if err != nil {
		return Addresses{}, fmt.Errorf("bind admin listener: %w", err)
	}
	g.adminListener = adminListener
	httpListener, err := net.Listen("tcp", g.httpServer.Addr)
	if err != nil {
		_ = adminListener.Close()
		return Addresses{}, fmt.Errorf("bind HTTP listener: %w", err)
	}
	g.httpListener = httpListener
	httpsBaseListener, err := net.Listen("tcp", g.httpsServer.Addr)
	if err != nil {
		_ = httpListener.Close()
		_ = adminListener.Close()
		return Addresses{}, fmt.Errorf("bind HTTPS listener: %w", err)
	}
	g.httpsListener = tls.NewListener(httpsBaseListener, g.tlsConfig)
	g.started = true

	g.serveWG.Add(3)
	go g.serve(g.adminServer, g.adminListener, "admin")
	go g.serve(g.httpServer, g.httpListener, "http")
	go g.serve(g.httpsServer, g.httpsListener, "https")
	go func() {
		g.serveWG.Wait()
		close(g.serveDone)
	}()
	g.telemetry.SetReady(true)

	return Addresses{
		HTTP:  g.httpListener.Addr().String(),
		HTTPS: g.httpsListener.Addr().String(),
		Admin: g.adminListener.Addr().String(),
	}, nil
}

func (g *Gateway) serve(server *http.Server, listener net.Listener, name string) {
	defer g.serveWG.Done()
	err := server.Serve(listener)
	if err == nil || errors.Is(err, http.ErrServerClosed) || errors.Is(err, net.ErrClosed) {
		return
	}
	g.telemetry.SetReady(false)
	wrapped := fmt.Errorf("serve %s listener: %w", name, err)
	select {
	case g.serveErrors <- wrapped:
	default:
	}
}

// Wait blocks after a successful Start until a server exits unexpectedly or
// all three servers stop. It returns the first reported serve error, or nil
// after orderly termination.
func (g *Gateway) Wait() error {
	select {
	case err := <-g.serveErrors:
		return err
	case <-g.serveDone:
		return nil
	}
}

// Shutdown begins an idempotent readiness-first shutdown. The first call's
// context bounds graceful traffic drain and runtime cleanup; later calls return
// that same result. On deadline expiry, traffic servers are force-closed before
// final runtime and admin cleanup.
func (g *Gateway) Shutdown(ctx context.Context) error {
	g.closing.Store(true)
	g.shutdownOnce.Do(func() {
		g.shutdownErr = g.shutdown(ctx)
	})
	return g.shutdownErr
}

func (g *Gateway) shutdown(ctx context.Context) error {
	g.telemetry.SetReady(false)
	g.manager.StopHealth()

	trafficErrors := make(chan error, 2)
	go func() { trafficErrors <- g.httpServer.Shutdown(ctx) }()
	go func() { trafficErrors <- g.httpsServer.Shutdown(ctx) }()
	errs := []error{<-trafficErrors, <-trafficErrors}
	if ctx.Err() != nil {
		errs = append(errs, g.httpServer.Close(), g.httpsServer.Close())
	}
	g.trafficRequests.Wait()
	managerCtx := ctx
	var managerCancel context.CancelFunc
	if ctx.Err() != nil {
		managerCtx, managerCancel = context.WithTimeout(context.Background(), 2*time.Second)
		defer managerCancel()
	}
	if err := g.manager.Close(managerCtx); err != nil {
		errs = append(errs, err)
	}
	g.lifecycle.ShutdownCleanup(g.manager.UpstreamStats())

	if err := g.adminServer.Shutdown(ctx); err != nil {
		errs = append(errs, err)
		if ctx.Err() != nil {
			errs = append(errs, g.adminServer.Close())
		}
	}
	return errors.Join(errs...)
}

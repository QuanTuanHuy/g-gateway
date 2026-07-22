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

	"github.com/QuanTuanHuy/g-gateway/internal/config"
	"github.com/QuanTuanHuy/g-gateway/internal/model"
	"github.com/QuanTuanHuy/g-gateway/internal/proxy"
	"github.com/QuanTuanHuy/g-gateway/internal/telemetry"
	"github.com/QuanTuanHuy/g-gateway/internal/upstream"
)

type Addresses struct {
	HTTP  string
	HTTPS string
	Admin string
}

type Gateway struct {
	httpServer  *http.Server
	httpsServer *http.Server
	adminServer *http.Server
	tlsConfig   *tls.Config

	httpListener  net.Listener
	httpsListener net.Listener
	adminListener net.Listener

	telemetry *telemetry.Telemetry
	upstream  *upstream.Runtime
	logger    *slog.Logger

	startMu     sync.Mutex
	started     bool
	serveWG     sync.WaitGroup
	serveDone   chan struct{}
	serveErrors chan error

	shutdownOnce sync.Once
	shutdownErr  error
}

func New(bootstrap config.BootstrapConfig, resources model.ResourceSet, logger *slog.Logger) (*Gateway, error) {
	if logger == nil {
		return nil, fmt.Errorf("logger must not be nil")
	}
	if len(resources.Routes) != 1 || len(resources.Upstreams) != 1 {
		return nil, fmt.Errorf("Phase 1 gateway requires exactly one route and one upstream")
	}
	certificate, err := tls.LoadX509KeyPair(bootstrap.HTTPS.CertificateFile, bootstrap.HTTPS.PrivateKeyFile)
	if err != nil {
		return nil, fmt.Errorf("load TLS key pair: %w", err)
	}
	upstreamRuntime, err := upstream.New(resources.Upstreams[0])
	if err != nil {
		return nil, fmt.Errorf("construct upstream runtime: %w", err)
	}
	proxyHandler, err := proxy.New(proxy.Options{
		Route:               resources.Routes[0],
		Target:              upstreamRuntime.Target(),
		Transport:           upstreamRuntime.RoundTripper(),
		MaxRequestBodyBytes: bootstrap.Server.MaxRequestBodyBytes,
		Logger:              logger,
	})
	if err != nil {
		upstreamRuntime.CloseIdleConnections()
		return nil, fmt.Errorf("construct proxy handler: %w", err)
	}
	telemetryRuntime, err := telemetry.New(bootstrap.Telemetry.RequestMetricsEnabled, bootstrap.Telemetry.ProfilingEnabled)
	if err != nil {
		upstreamRuntime.CloseIdleConnections()
		return nil, fmt.Errorf("construct telemetry: %w", err)
	}

	httpProtocols := new(http.Protocols)
	httpProtocols.SetHTTP1(true)
	tlsProtocols := new(http.Protocols)
	tlsProtocols.SetHTTP1(true)
	tlsProtocols.SetHTTP2(true)
	adminProtocols := new(http.Protocols)
	adminProtocols.SetHTTP1(true)

	trafficHandler := recoverPanics(telemetryRuntime.Wrap(proxyHandler, resources.Routes[0].ID), logger)
	tlsConfig := &tls.Config{
		Certificates: []tls.Certificate{certificate},
		MinVersion:   tls.VersionTLS12,
		NextProtos:   []string{"h2", "http/1.1"},
	}
	gateway := &Gateway{
		tlsConfig:   tlsConfig,
		telemetry:   telemetryRuntime,
		upstream:    upstreamRuntime,
		logger:      logger,
		serveDone:   make(chan struct{}),
		serveErrors: make(chan error, 3),
	}
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

func (g *Gateway) Wait() error {
	select {
	case err := <-g.serveErrors:
		return err
	case <-g.serveDone:
		return nil
	}
}

func (g *Gateway) Shutdown(ctx context.Context) error {
	g.shutdownOnce.Do(func() {
		g.shutdownErr = g.shutdown(ctx)
	})
	return g.shutdownErr
}

func (g *Gateway) shutdown(ctx context.Context) error {
	g.telemetry.SetReady(false)

	trafficErrors := make(chan error, 2)
	go func() { trafficErrors <- g.httpServer.Shutdown(ctx) }()
	go func() { trafficErrors <- g.httpsServer.Shutdown(ctx) }()
	errs := []error{<-trafficErrors, <-trafficErrors}
	if ctx.Err() != nil {
		errs = append(errs, g.httpServer.Close(), g.httpsServer.Close())
	}
	g.upstream.CloseIdleConnections()

	if err := g.adminServer.Shutdown(ctx); err != nil {
		errs = append(errs, err)
		if ctx.Err() != nil {
			errs = append(errs, g.adminServer.Close())
		}
	}
	return errors.Join(errs...)
}

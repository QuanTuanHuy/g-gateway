// Package main implements the gateway-dp command, which runs the G-Gateway data
// plane from a versioned configuration file until shutdown or an unrecoverable
// listener error.
package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/QuanTuanHuy/g-gateway/internal/config"
	"github.com/QuanTuanHuy/g-gateway/internal/gateway"
)

func main() {
	os.Exit(run(os.Args[1:], os.Stderr))
}

func run(arguments []string, stderr io.Writer) int {
	logger := slog.New(slog.NewJSONHandler(stderr, nil)).With("component", "gateway-dp")
	flags := flag.NewFlagSet("gateway-dp", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	configFile := flags.String("config", "configs/phase1.yaml", "path to the gateway configuration")
	if err := flags.Parse(arguments); err != nil {
		return startupFailure(logger, "parse_flags", err)
	}
	if flags.NArg() != 0 {
		return startupFailure(logger, "parse_flags", fmt.Errorf("unexpected positional arguments: %v", flags.Args()))
	}

	bootstrap, resources, err := config.Load(*configFile)
	if err != nil {
		return startupFailure(logger, "load_config", err)
	}
	instance, err := gateway.New(bootstrap, resources, logger)
	if err != nil {
		return startupFailure(logger, "construct_gateway", err)
	}
	addresses, err := instance.Start()
	if err != nil {
		return startupFailure(logger, "start_gateway", err)
	}
	logger.Info("gateway started", "http_address", addresses.HTTP, "https_address", addresses.HTTPS, "admin_address", addresses.Admin)

	signalContext, stopSignals := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stopSignals()
	waitResult := make(chan error, 1)
	go func() { waitResult <- instance.Wait() }()

	select {
	case <-signalContext.Done():
		shutdownContext, cancel := context.WithTimeout(context.Background(), bootstrap.Server.ShutdownTimeout)
		shutdownErr := instance.Shutdown(shutdownContext)
		cancel()
		waitErr := <-waitResult
		if shutdownErr != nil {
			logger.Error("gateway shutdown failed", "error", shutdownErr)
			return 1
		}
		if waitErr != nil {
			logger.Error("gateway serve failed during shutdown", "error", waitErr)
			return 1
		}
		logger.Info("gateway stopped")
		return 0
	case waitErr := <-waitResult:
		shutdownContext, cancel := context.WithTimeout(context.Background(), bootstrap.Server.ShutdownTimeout)
		shutdownErr := instance.Shutdown(shutdownContext)
		cancel()
		if waitErr == nil {
			waitErr = fmt.Errorf("all gateway listeners stopped unexpectedly")
		}
		logger.Error("gateway serve failed", "error", waitErr)
		if shutdownErr != nil {
			logger.Error("gateway shutdown after serve failure failed", "error", shutdownErr)
		}
		return 1
	}
}

func startupFailure(logger *slog.Logger, stage string, err error) int {
	logger.Error("gateway startup failed", "stage", stage, "error", err)
	return 1
}

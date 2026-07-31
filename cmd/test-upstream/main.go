// Package main implements the test-upstream command, which runs the
// deterministic HTTP upstream used by correctness and integration tests.
package main

import (
	"context"
	"errors"
	"flag"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/QuanTuanHuy/g-gateway/internal/testupstream"
)

func main() {
	os.Exit(run(os.Args[1:], os.Stderr))
}

func run(arguments []string, stderr io.Writer) int {
	logger := slog.New(slog.NewJSONHandler(stderr, nil)).With("component", "test-upstream")
	flags := flag.NewFlagSet("test-upstream", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	listenAddress := flags.String("listen", ":8080", "HTTP listen address")
	if err := flags.Parse(arguments); err != nil || flags.NArg() != 0 {
		logger.Error("test upstream flag parsing failed", "error", err, "arguments", flags.Args())
		return 1
	}

	server := &http.Server{
		Addr:              *listenAddress,
		Handler:           testupstream.New(logger),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       35 * time.Second,
		WriteTimeout:      35 * time.Second,
		IdleTimeout:       60 * time.Second,
		MaxHeaderBytes:    1 << 20,
	}
	serveResult := make(chan error, 1)
	go func() { serveResult <- server.ListenAndServe() }()
	logger.Info("test upstream started", "address", *listenAddress)

	signalContext, stopSignals := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stopSignals()
	select {
	case <-signalContext.Done():
		shutdownContext, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		shutdownErr := server.Shutdown(shutdownContext)
		cancel()
		serveErr := <-serveResult
		if shutdownErr != nil {
			logger.Error("test upstream shutdown failed", "error", shutdownErr)
			return 1
		}
		if serveErr != nil && !errors.Is(serveErr, http.ErrServerClosed) {
			logger.Error("test upstream serve failed during shutdown", "error", serveErr)
			return 1
		}
		logger.Info("test upstream stopped")
		return 0
	case serveErr := <-serveResult:
		if serveErr == nil || errors.Is(serveErr, http.ErrServerClosed) {
			serveErr = errors.New("test upstream stopped unexpectedly")
		}
		logger.Error("test upstream serve failed", "error", serveErr)
		return 1
	}
}

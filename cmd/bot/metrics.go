package main

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"time"
)

type metricsHandler interface {
	Handler() http.Handler
}

func runMetricsServer(ctx context.Context, log *slog.Logger, address string, metrics metricsHandler) {
	if address == "" || metrics == nil {
		return
	}
	listener, err := net.Listen("tcp", address)
	if err != nil {
		log.Warn("metrics server unavailable", "address", address, "error", err)
		return
	}
	server := &http.Server{Handler: metrics.Handler(), ReadHeaderTimeout: 5 * time.Second}
	log.Info("metrics server listening", "address", address)
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := server.Shutdown(shutdownCtx); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Warn("metrics server shutdown failed", "error", err)
		}
	}()
	if err := server.Serve(listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Warn("metrics server stopped", "error", err)
	}
}

package main

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"sync"
	"time"

	"bybit_bot_liquidity/internal/observability"
)

// startMetricsServer запускает необязательный локальный listener и связывает его отмену с циклом.
func startMetricsServer(ctx context.Context, log *slog.Logger, address string, registry *observability.Registry, background *sync.WaitGroup) {
	if address == "" {
		return
	}
	listener, err := net.Listen("tcp", address)
	if err != nil {
		log.Error("metrics listener failed", "error", err)
		return
	}
	server := &http.Server{Handler: registry.Handler(), ReadHeaderTimeout: 5 * time.Second}
	background.Add(1)
	go func() {
		defer background.Done()
		serveDone := make(chan error, 1)
		go func() {
			serveDone <- server.Serve(listener)
		}()
		select {
		case err := <-serveDone:
			if err != nil && !errors.Is(err, http.ErrServerClosed) {
				log.Error("metrics server failed", "error", err)
			}
		case <-ctx.Done():
			shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			if err := server.Shutdown(shutdownCtx); err != nil {
				log.Error("metrics server shutdown failed", "error", err)
				if closeErr := server.Close(); closeErr != nil {
					log.Error("metrics server close failed", "error", closeErr)
				}
			}
			if err := <-serveDone; err != nil && !errors.Is(err, http.ErrServerClosed) {
				log.Error("metrics server failed", "error", err)
			}
		}
	}()
}

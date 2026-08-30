package main

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"

	"bybit_bot_liquidity/internal/exchange"
	"bybit_bot_liquidity/internal/execution"
)

func TestReportExecutionReadinessSkipsWithoutCredentials(t *testing.T) {
	t.Parallel()

	var output bytes.Buffer
	reportExecutionReadiness(
		context.Background(),
		slog.New(slog.NewTextHandler(&output, nil)),
		false,
		nil,
		"BTCUSDT",
	)
	if output.Len() != 0 {
		t.Fatalf("log = %q, want empty", output.String())
	}
}

func TestReportExecutionReadinessLogsSuccessWithoutAccountValues(t *testing.T) {
	t.Parallel()

	var output bytes.Buffer
	checker := &executionReadinessCheckerStub{
		readiness: execution.ExecutionReadiness{
			Symbol: "BTCUSDT",
			Balance: exchange.AccountBalance{
				AvailableUSD: 98765.4321,
			},
			Leverage: exchange.LeverageInfo{Leverage: 37},
		},
	}
	reportExecutionReadiness(
		context.Background(),
		slog.New(slog.NewTextHandler(&output, nil)),
		true,
		checker,
		"BTCUSDT",
	)

	logged := output.String()
	if !strings.Contains(logged, "execution readiness confirmed") ||
		!strings.Contains(logged, "symbol=BTCUSDT") {
		t.Fatalf("log = %q", logged)
	}
	if strings.Contains(logged, "98765.4321") || strings.Contains(logged, "leverage=37") {
		t.Fatalf("log exposes account values: %q", logged)
	}
	if checker.calls != 1 || checker.symbol != "BTCUSDT" {
		t.Fatalf("checker calls/symbol = %d/%q, want 1/BTCUSDT", checker.calls, checker.symbol)
	}
}

func TestReportExecutionReadinessLogsFailureAndContinues(t *testing.T) {
	t.Parallel()

	var output bytes.Buffer
	expected := errors.New("readiness unavailable")
	checker := &executionReadinessCheckerStub{err: expected}
	reportExecutionReadiness(
		context.Background(),
		slog.New(slog.NewTextHandler(&output, nil)),
		true,
		checker,
		"BTCUSDT",
	)

	logged := output.String()
	if !strings.Contains(logged, "execution readiness failed") ||
		!strings.Contains(logged, "symbol=BTCUSDT") ||
		!strings.Contains(logged, "reason=source_error") {
		t.Fatalf("log = %q", logged)
	}
	if strings.Contains(logged, expected.Error()) {
		t.Fatalf("log exposes source error: %q", logged)
	}
	if checker.calls != 1 {
		t.Fatalf("checker calls = %d, want 1", checker.calls)
	}
}

func TestReportExecutionReadinessHandlesMissingChecker(t *testing.T) {
	t.Parallel()

	var output bytes.Buffer
	reportExecutionReadiness(
		context.Background(),
		slog.New(slog.NewTextHandler(&output, nil)),
		true,
		nil,
		"BTCUSDT",
	)
	if logged := output.String(); !strings.Contains(logged, "reason=source_required") {
		t.Fatalf("log = %q", logged)
	}
}

func TestClassifyExecutionReadinessError(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		err    error
		reason string
	}{
		{name: "nil", reason: ""},
		{name: "canceled", err: context.Canceled, reason: "canceled"},
		{name: "timeout", err: context.DeadlineExceeded, reason: "timeout"},
		{
			name:   "wrapped margin mode",
			err:    errors.Join(errors.New("preflight failed"), execution.ErrUnsupportedMarginMode),
			reason: "unsupported_margin_mode",
		},
		{name: "position", err: execution.ErrPositionAlreadyOpen, reason: "position_already_open"},
		{name: "source", err: errors.New("private payload"), reason: "source_error"},
	}

	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			if got := classifyExecutionReadinessError(test.err); got != test.reason {
				t.Fatalf("classifyExecutionReadinessError() = %q, want %q", got, test.reason)
			}
		})
	}
}

type executionReadinessCheckerStub struct {
	readiness execution.ExecutionReadiness
	err       error
	symbol    string
	calls     int
}

func (s *executionReadinessCheckerStub) Check(_ context.Context, symbol string) (execution.ExecutionReadiness, error) {
	s.calls++
	s.symbol = symbol
	return s.readiness, s.err
}

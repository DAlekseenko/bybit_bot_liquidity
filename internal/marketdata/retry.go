package marketdata

import (
	"context"
	"errors"
	"math/rand/v2"
	"net"
	"time"
)

const (
	defaultRetryAttempts = 3
	defaultRetryInitial  = 250 * time.Millisecond
	defaultRetryMaximum  = 2 * time.Second
)

// RetryPolicy bounds retries for idempotent market-data reads. It never retries
// cancelled contexts or errors not explicitly classified as transient.
type RetryPolicy struct {
	MaxAttempts   int
	InitialDelay  time.Duration
	MaxDelay      time.Duration
	JitterPercent float64
}

func DefaultRetryPolicy() RetryPolicy {
	return RetryPolicy{
		MaxAttempts:   defaultRetryAttempts,
		InitialDelay:  defaultRetryInitial,
		MaxDelay:      defaultRetryMaximum,
		JitterPercent: 0.2,
	}
}

func (p RetryPolicy) valid() bool {
	return p.MaxAttempts >= 1 && p.InitialDelay > 0 && p.MaxDelay >= p.InitialDelay && p.JitterPercent >= 0 && p.JitterPercent <= 1
}

type retryable interface {
	Retryable() bool
}

func (p RetryPolicy) do(ctx context.Context, call func() error, sleep func(context.Context, time.Duration) error, random func() float64) error {
	var err error
	for attempt := 1; attempt <= p.MaxAttempts; attempt++ {
		err = call()
		if err == nil || !isRetryable(err) || attempt == p.MaxAttempts {
			return err
		}
		if err := sleep(ctx, p.delay(attempt, random)); err != nil {
			return err
		}
	}
	return err
}

func (p RetryPolicy) delay(attempt int, random func() float64) time.Duration {
	delay := p.InitialDelay
	for i := 1; i < attempt && delay < p.MaxDelay; i++ {
		delay *= 2
		if delay > p.MaxDelay {
			delay = p.MaxDelay
		}
	}
	if p.JitterPercent == 0 {
		return delay
	}
	// Symmetric bounded jitter prevents synchronized retries from many instances.
	factor := 1 - p.JitterPercent + 2*p.JitterPercent*random()
	return time.Duration(float64(delay) * factor)
}

func isRetryable(err error) bool {
	if err == nil || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return false
	}
	var classified retryable
	if errors.As(err, &classified) {
		return classified.Retryable()
	}
	var networkErr net.Error
	return errors.As(err, &networkErr) && (networkErr.Timeout() || networkErr.Temporary())
}

func sleepWithContext(ctx context.Context, delay time.Duration) error {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func randomFloat() float64 { return rand.Float64() }

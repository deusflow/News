package retry

import (
	"context"
	"fmt"
	"time"
)

type RetryConfig struct {
	MaxAttempts int
	Delay       time.Duration
	Backoff     bool // Exponential backoff
}

func WithRetry(ctx context.Context, config RetryConfig, fn func() error) error {
	var lastErr error

	for attempt := 1; attempt <= config.MaxAttempts; attempt++ {
		if err := fn(); err != nil {
			lastErr = err

			if attempt == config.MaxAttempts {
				return fmt.Errorf("failed after %d attempts: %w", config.MaxAttempts, err)
			}

			delay := config.Delay
			if config.Backoff {
				delay = time.Duration(attempt) * config.Delay
			}

			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(delay):
				continue
			}
		}
		return nil
	}

	return lastErr
}

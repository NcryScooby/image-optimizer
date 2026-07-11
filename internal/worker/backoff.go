package worker

import (
	"context"
	"time"
)

// backoffDuration returns the exponential delay before requeue.
// Schedule: 1s * 2^(attempts-1) → attempts 1→1s, 2→2s, 3→4s.
func backoffDuration(attempts int) time.Duration {
	if attempts < 1 {
		attempts = 1
	}
	return time.Second * time.Duration(1<<(attempts-1))
}

// waitBackoff sleeps for backoffDuration(attempts) or until ctx is cancelled.
func waitBackoff(ctx context.Context, attempts int) error {
	timer := time.NewTimer(backoffDuration(attempts))
	defer timer.Stop()
	select {
	case <-timer.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

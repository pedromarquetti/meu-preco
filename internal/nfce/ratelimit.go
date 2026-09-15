package nfce

import (
	"context"
	"sync"
	"time"
)

// rateLimiter is a token-bucket paced by an interval between releases (burst
// of 1). It exists to stay polite to the fiscal portal: one request per token.
type rateLimiter struct {
	mu       sync.Mutex
	interval time.Duration
	next     time.Time
}

func newRateLimiter(rps int) *rateLimiter {
	interval := time.Second / time.Duration(rps)
	if interval <= 0 {
		interval = time.Nanosecond
	}
	return &rateLimiter{interval: interval}
}

// Wait blocks until a token is available or ctx is done.
func (r *rateLimiter) Wait(ctx context.Context) error {
	r.mu.Lock()
	now := time.Now()
	wait := r.next.Sub(now)
	if wait <= 0 {
		r.next = now.Add(r.interval)
		r.mu.Unlock()
		return nil
	}
	r.next = r.next.Add(r.interval)
	r.mu.Unlock()

	timer := time.NewTimer(wait)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

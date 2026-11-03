package nfce

import (
	"context"
	"sync"
	"time"
)

// rateLimiter is a token-bucket paced by an interval between releases (burst
// of 1). It exists to stay polite to the fiscal portal: one request per token.
//
// It also carries a shared cooldown deadline: when the portal serves an
// anti-bot/rate-limit rejection page, every request for the same source waits
// until that deadline before touching the portal again.
type rateLimiter struct {
	mu            sync.Mutex
	interval      time.Duration
	next          time.Time
	cooldownUntil time.Time
}

func newRateLimiter(rps int) *rateLimiter {
	interval := time.Second / time.Duration(rps)
	if interval <= 0 {
		interval = time.Nanosecond
	}
	return &rateLimiter{interval: interval}
}

// Wait blocks until a token is available and any active cooldown has expired,
// or ctx is done. The slot is reserved up-front so concurrent callers queue
// instead of racing for the same token. If another request extends the
// cooldown while this one waits, the wait is extended too.
func (r *rateLimiter) Wait(ctx context.Context) error {
	r.mu.Lock()
	now := time.Now()
	slot := r.next
	if r.cooldownUntil.After(slot) {
		slot = r.cooldownUntil
	}
	if slot.Before(now) {
		slot = now
	}
	r.next = slot.Add(r.interval)
	r.mu.Unlock()

	for {
		now = time.Now()
		if !slot.After(now) {
			return nil
		}

		// The cooldown may have been extended after this caller reserved its
		// slot, so re-read it before sleeping and after every wake-up.
		r.mu.Lock()
		if r.cooldownUntil.After(slot) {
			slot = r.cooldownUntil
		}
		r.mu.Unlock()

		timer := time.NewTimer(slot.Sub(now))
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
}

// Cooldown records that the portal is blocking requests for at least d. A
// later rejection may extend the deadline but never shortens it. It returns
// the effective deadline and whether it moved forward, so callers can log the
// transition.
func (r *rateLimiter) Cooldown(d time.Duration) (time.Time, bool) {
	if d <= 0 {
		return r.CooldownUntil(), false
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	until := time.Now().Add(d)
	if !until.After(r.cooldownUntil) {
		return r.cooldownUntil, false
	}
	r.cooldownUntil = until
	return until, true
}

// CooldownUntil returns the current cooldown deadline, or the zero time when
// no cooldown is active.
func (r *rateLimiter) CooldownUntil() time.Time {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.cooldownUntil
}

package nfce

import (
	"context"
	"errors"
	"sync"
	"time"
)

// PortalLimiter is a token-bucket paced by an interval between releases (burst
// of 1). It exists to stay polite to the fiscal portal: one request per token.
//
// It also carries a shared cooldown deadline: when the portal serves an
// anti-bot/rate-limit rejection page or drops the connection at its edge,
// every request for the same source refuses to touch the portal until that
// deadline instead of sleeping through it (see Wait).
//
// One limiter belongs to one portal host. The fetcher and the captcha solver
// must share it so a rejection on either path stops the other too.
type PortalLimiter struct {
	mu            sync.Mutex
	interval      time.Duration
	next          time.Time
	cooldownUntil time.Time
}

// NewPortalLimiter paces requests at max(1/rps, minInterval). minInterval exists
// because the SEFAZ edge blocks a source quickly: 1 RPS is still too fast for a
// bot-defense-protected portal, and rps alone cannot express sub-1Hz pacing.
func NewPortalLimiter(rps int, minInterval time.Duration) *PortalLimiter {
	if rps < 1 {
		rps = 1
	}
	interval := time.Second / time.Duration(rps)
	if interval <= 0 {
		interval = time.Nanosecond
	}
	if minInterval > interval {
		interval = minInterval
	}
	return &PortalLimiter{interval: interval}
}

// Wait blocks until a token is available, or returns a typed error when the
// portal is cooling down or the context deadline cannot be met.
//
// An active cooldown fails fast: a caller must never sit through the default
// one-minute cooldown inside an HTTP handler (the server write timeout is
// shorter). The slot is reserved up-front so concurrent callers queue instead
// of racing for the same token.
func (r *PortalLimiter) Wait(ctx context.Context) error {
	r.mu.Lock()
	now := time.Now()
	if r.cooldownUntil.After(now) {
		err := newCooldownError(r.cooldownUntil, now)
		r.mu.Unlock()
		return err
	}
	slot := r.next
	if slot.Before(now) {
		slot = now
	}
	r.next = slot.Add(r.interval)
	r.mu.Unlock()

	for {
		now = time.Now()

		// A cooldown may have been applied after this caller reserved its slot;
		// re-read it before sleeping so it also fails fast.
		r.mu.Lock()
		if r.cooldownUntil.After(now) {
			err := newCooldownError(r.cooldownUntil, now)
			r.mu.Unlock()
			return err
		}
		r.mu.Unlock()

		if !slot.After(now) {
			return nil
		}

		timer := time.NewTimer(slot.Sub(now))
		select {
		case <-ctx.Done():
			timer.Stop()
			if errors.Is(ctx.Err(), context.DeadlineExceeded) {
				retry := time.Until(slot)
				if retry <= 0 {
					retry = time.Millisecond
				}
				return &PortalBusyError{RetryAfter: retry}
			}
			return ctx.Err()
		case <-timer.C:
		}
	}
}

// Cooldown records that the portal is blocking requests for at least d. A
// later rejection may extend the deadline but never shortens it. It returns
// the effective deadline and whether it moved forward, so callers can log the
// transition.
func (r *PortalLimiter) Cooldown(d time.Duration) (time.Time, bool) {
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
func (r *PortalLimiter) CooldownUntil() time.Time {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.cooldownUntil
}

// cooldownError builds the typed fail-fast error for an active cooldown.
// Callers that already recorded the cooldown can return it directly.
func (r *PortalLimiter) cooldownError() *PortalCooldownError {
	r.mu.Lock()
	defer r.mu.Unlock()
	return newCooldownError(r.cooldownUntil, time.Now())
}

// newCooldownError returns nil when the deadline is not in the future.
func newCooldownError(until, now time.Time) *PortalCooldownError {
	if !until.After(now) {
		return nil
	}
	return &PortalCooldownError{Until: until, RetryAfter: until.Sub(now)}
}

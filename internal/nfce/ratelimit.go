package nfce

import (
	"context"
	"errors"
	"sync"
	"time"
)

// DefaultPortalQueueMaxWait bounds how long a request may wait for its pacing
// slot when no explicit budget is configured. It stays below the HTTP layer's
// portal request budget (and well below the server write timeout), so a
// saturated queue answers with a retryable error instead of holding the
// connection open.
const DefaultPortalQueueMaxWait = 10 * time.Second

// PortalLimiter is a token-bucket paced by an interval between releases (burst
// of 1). It exists to stay polite to the fiscal portal: one request per token.
//
// It also carries a shared cooldown deadline: when the portal serves an
// anti-bot/rate-limit rejection page or drops the connection at its edge,
// every request for the same source refuses to touch the portal until that
// deadline instead of sleeping through it (see Wait).
//
// Finally, it bounds the pacing queue: a caller whose slot is further away
// than maxWait fails fast with a *PortalBusyError instead of occupying a
// handler goroutine (see Wait).
//
// One limiter belongs to one portal host. The fetcher and the captcha solver
// must share it so a rejection on either path stops the other too.
type PortalLimiter struct {
	mu            sync.Mutex
	interval      time.Duration
	maxWait       time.Duration
	next          time.Time
	cooldownUntil time.Time
}

// NewPortalLimiter paces requests at max(1/rps, minInterval) and caps the
// queue wait at DefaultPortalQueueMaxWait. minInterval exists because the SEFAZ
// edge blocks a source quickly: 1 RPS is still too fast for a
// bot-defense-protected portal, and rps alone cannot express sub-1Hz pacing.
func NewPortalLimiter(rps int, minInterval time.Duration) *PortalLimiter {
	return NewPortalLimiterWithMaxWait(rps, minInterval, DefaultPortalQueueMaxWait)
}

// NewPortalLimiterWithMaxWait is NewPortalLimiter with an explicit queue
// budget: a request whose slot is more than maxWait away fails immediately.
// maxWait <= 0 disables queueing entirely (only an immediately available slot
// is accepted). It is used by tests and callers that need a tighter budget
// than the default.
func NewPortalLimiterWithMaxWait(rps int, minInterval, maxWait time.Duration) *PortalLimiter {
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
	if maxWait < 0 {
		maxWait = 0
	}
	return &PortalLimiter{interval: interval, maxWait: maxWait}
}

// Wait blocks until a token is available, or returns a typed error when the
// portal is cooling down, the queue is saturated, or the context deadline
// cannot be met.
//
// An active cooldown fails fast: a caller must never sit through the default
// one-minute cooldown inside an HTTP handler (the server write timeout is
// shorter). A slot beyond maxWait (or beyond the caller's context deadline)
// also fails fast, and it is rejected *before* the slot is reserved so
// saturated requests do not inflate the queue for everyone else. Accepted
// callers reserve their slot up-front so they queue instead of racing for the
// same token.
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
	wait := slot.Sub(now)
	if wait > r.maxWait {
		r.mu.Unlock()
		return &PortalBusyError{RetryAfter: wait}
	}
	if deadline, ok := ctx.Deadline(); ok && slot.After(deadline) {
		r.mu.Unlock()
		return &PortalBusyError{RetryAfter: wait}
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

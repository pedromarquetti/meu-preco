package auth

import (
	"sync"
	"time"
)

// loginThrottleCap bounds the number of tracked login identifiers. Random
// login strings must not be able to grow the map without limit.
const loginThrottleCap = 10_000

// loginThrottleEntry is a fixed window of failed attempts for one identifier.
type loginThrottleEntry struct {
	failures int
	resetAt  time.Time
	lastSeen time.Time
}

// LoginThrottle bounds failed logins per account identifier. It is a
// process-local, fixed-window tracker: RecordFailure creates/advances a window
// only when it expires, Allowed never extends it, and Reset clears a
// successful identifier.
//
// A zero max or window disables throttling (used by tests and by explicitly
// disabled deployments). The zero value is not usable; construct it with
// NewLoginThrottle.
type LoginThrottle struct {
	max       int
	window    time.Duration
	cap       int
	sweep     time.Duration
	now       func() time.Time
	mu        sync.Mutex
	entries   map[string]loginThrottleEntry
	lastSweep time.Time
}

// NewLoginThrottle builds a throttle that allows max failures per key inside
// window.
func NewLoginThrottle(max int, window time.Duration) *LoginThrottle {
	return newLoginThrottle(max, window, time.Now)
}

func newLoginThrottle(max int, window time.Duration, now func() time.Time) *LoginThrottle {
	sweep := time.Minute
	if window > 0 && window < sweep {
		sweep = window
	}

	return &LoginThrottle{
		max:     max,
		window:  window,
		cap:     loginThrottleCap,
		sweep:   sweep,
		now:     now,
		entries: make(map[string]loginThrottleEntry),
	}
}

func (t *LoginThrottle) enabled() bool {
	return t != nil && t.max > 0 && t.window > 0
}

// Allowed reports whether key may attempt another login. It does not extend
// the failure window.
func (t *LoginThrottle) Allowed(key string) bool {
	if !t.enabled() {
		return true
	}

	t.mu.Lock()
	defer t.mu.Unlock()

	now := t.now()
	t.sweepLocked(now)

	e, ok := t.entries[key]
	if !ok || !now.Before(e.resetAt) {
		if ok {
			delete(t.entries, key)
		}
		return true
	}

	return e.failures < t.max
}

// RetryAfter returns how long key must wait before another attempt. It returns
// zero when the key is not throttled or the window has expired.
func (t *LoginThrottle) RetryAfter(key string) time.Duration {
	if !t.enabled() {
		return 0
	}

	t.mu.Lock()
	defer t.mu.Unlock()

	now := t.now()
	e, ok := t.entries[key]
	if !ok || !now.Before(e.resetAt) {
		if ok {
			delete(t.entries, key)
		}
		return 0
	}

	return e.resetAt.Sub(now)
}

// RecordFailure adds one failure for key, creating a window on first use. When
// the map is full (after sweeping expired entries) the oldest lastSeen entry is
// evicted.
func (t *LoginThrottle) RecordFailure(key string) {
	if !t.enabled() {
		return
	}

	t.mu.Lock()
	defer t.mu.Unlock()

	now := t.now()
	t.sweepLocked(now)

	e, ok := t.entries[key]
	if ok && !now.Before(e.resetAt) {
		delete(t.entries, key)
		ok = false
	}

	if !ok {
		if len(t.entries) >= t.cap {
			t.evictOldestLocked()
		}
		e = loginThrottleEntry{resetAt: now.Add(t.window)}
	}

	e.failures++
	e.lastSeen = now
	t.entries[key] = e
}

// Reset clears the failures for key (a successful login).
func (t *LoginThrottle) Reset(key string) {
	if !t.enabled() {
		return
	}

	t.mu.Lock()
	defer t.mu.Unlock()
	delete(t.entries, key)
}

// sweepLocked drops expired entries, at most once per sweep interval so the
// per-request cost stays bounded.
func (t *LoginThrottle) sweepLocked(now time.Time) {
	if t.sweep <= 0 {
		return
	}
	if !t.lastSweep.IsZero() && now.Sub(t.lastSweep) < t.sweep {
		return
	}
	t.lastSweep = now

	for key, e := range t.entries {
		if !now.Before(e.resetAt) {
			delete(t.entries, key)
		}
	}
}

// evictOldestLocked removes the entry with the oldest lastSeen. Callers hold
// t.mu.
func (t *LoginThrottle) evictOldestLocked() {
	var (
		oldestKey  string
		oldestSeen time.Time
		found      bool
	)

	for key, e := range t.entries {
		if !found || e.lastSeen.Before(oldestSeen) {
			oldestKey, oldestSeen, found = key, e.lastSeen, true
		}
	}
	if found {
		delete(t.entries, oldestKey)
	}
}

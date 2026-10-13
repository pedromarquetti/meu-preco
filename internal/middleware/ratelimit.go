package middleware

import (
	"net"
	"net/http"
	"sync"
	"time"

	"meupreco/internal/httpx"
)

// RateLimitKey derives the limiter key (e.g. "ip:1.2.3.4" or "user:7") for a
// request. An empty key means "not limited".
type RateLimitKey func(r *http.Request) string

// ClientIP returns the client's IP with any port stripped. It reads the peer
// address from the request (RemoteAddr); it does not trust X-Forwarded-For.
func ClientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

// IPKey limits by client IP.
func IPKey(r *http.Request) string {
	return "ip:" + ClientIP(r)
}

// tokenBucket is a fixed-capacity bucket refilled at a steady rate. A request
// consumes one token; a full bucket accepts burst requests back-to-back.
type tokenBucket struct {
	mu       sync.Mutex
	capacity float64
	refill   float64 // tokens per second
	tokens   float64
	last     time.Time
	now      func() time.Time
}

func newTokenBucket(refill float64, burst int, now func() time.Time) *tokenBucket {
	t := now()
	return &tokenBucket{
		capacity: float64(burst),
		refill:   refill,
		tokens:   float64(burst),
		last:     t,
		now:      now,
	}
}

// allow consumes one token when available and reports whether the request may
// proceed.
func (b *tokenBucket) allow() bool {
	b.mu.Lock()
	defer b.mu.Unlock()

	now := b.now()
	if d := now.Sub(b.last).Seconds(); d > 0 {
		b.tokens = min(b.capacity, b.tokens+d*b.refill)
		b.last = now
	}
	if b.tokens < 1 {
		return false
	}
	b.tokens--
	return true
}

// keyedLimiter owns a per-key token bucket map with a lazy idle sweep so
// long-lived clients cannot accumulate infinite buckets.
type keyedLimiter struct {
	mu         sync.Mutex
	buckets    map[string]*tokenBucket
	refill     float64
	burst      int
	now        func() time.Time
	sweepEvery time.Duration
	lastSweep  time.Time
}

func newKeyedLimiter(refill float64, burst int, now func() time.Time) *keyedLimiter {
	return &keyedLimiter{
		buckets:    make(map[string]*tokenBucket),
		refill:     refill,
		burst:      burst,
		now:        now,
		sweepEvery: 5 * time.Minute,
		lastSweep:  now(),
	}
}

func (l *keyedLimiter) allow(key string) bool {
	l.mu.Lock()
	now := l.now()
	if now.Sub(l.lastSweep) >= l.sweepEvery {
		for k, b := range l.buckets {
			if now.Sub(b.last) >= l.sweepEvery {
				delete(l.buckets, k)
			}
		}
		l.lastSweep = now
	}
	b, ok := l.buckets[key]
	if !ok {
		b = newTokenBucket(l.refill, l.burst, l.now)
		l.buckets[key] = b
	}
	l.mu.Unlock()
	return b.allow()
}

// KeyedRateLimit enforces a shared token-bucket budget per key derived from
// the request. When refill <= 0 or burst <= 0 the middleware is a pass-through
// (limits disabled). A request over budget answers 429 with the canonical
// "rate_limited" envelope and a Retry-After hint.
func KeyedRateLimit(refill float64, burst int, key RateLimitKey) func(http.Handler) http.Handler {
	if refill <= 0 || burst <= 0 {
		return func(next http.Handler) http.Handler { return next }
	}

	limiter := newKeyedLimiter(refill, burst, time.Now)

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			k := key(r)
			if k != "" && !limiter.allow(k) {
				w.Header().Set("Retry-After", "1")
				httpx.Error(w, r, http.StatusTooManyRequests,
					"rate_limited", "request rate limit exceeded; slow down and retry")
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

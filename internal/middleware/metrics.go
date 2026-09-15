package middleware

import (
	"sync/atomic"
	"time"
)

// Package-level request metrics. Phase 8 will expose these through a
// /metrics endpoint; for now they are recorded and asserted in tests.
var (
	totalRequests   atomic.Int64
	totalDurationMs atomic.Int64
)

// Metrics is a point-in-time snapshot of the request counters.
type Metrics struct {
	TotalRequests   int64
	TotalDurationMs int64
}

// Snapshot returns the current request metrics.
func Snapshot() Metrics {
	return Metrics{
		TotalRequests:   totalRequests.Load(),
		TotalDurationMs: totalDurationMs.Load(),
	}
}

// recordRequest updates the package counters for a completed request.
func recordRequest(_ int, dur time.Duration) {
	totalRequests.Add(1)
	totalDurationMs.Add(dur.Milliseconds())
}

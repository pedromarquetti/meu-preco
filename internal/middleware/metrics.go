package middleware

import (
	"net/http"
	"sort"
	"sync"
	"time"
)

// latencyBucketsMs are the upper bounds (in ms) of the latency histogram; the
// last bucket absorbs every slower request.
var latencyBucketsMs = []int64{5, 10, 25, 50, 100, 250, 500, 1000, 2500, 5000, 10000, 30000}

// RouteMetrics is the per-route snapshot: how often a (method, pattern) was
// served, how often it errored, and its latency histogram. Histogram is the
// count per latencyBucketsMs bin (one extra overflow bin at the end).
type RouteMetrics struct {
	Method       string  `json:"method"`
	Pattern      string  `json:"pattern"`
	Count        int64   `json:"count"`
	Errors       int64   `json:"errors"`
	AuthFailures int64   `json:"auth_failures"`
	TotalMs      int64   `json:"total_ms"`
	Histogram    []int64 `json:"histogram"`
}

// MetricsReport is the serializable report the /metrics endpoint serves.
type MetricsReport struct {
	TotalRequests   int64            `json:"total_requests"`
	TotalDurationMs int64            `json:"total_duration_ms"`
	AuthFailures    int64            `json:"auth_failures"`
	ByStatus        map[string]int64 `json:"by_status"`
	Routes          []RouteMetrics   `json:"routes"`
	LatencyBuckets  []int64          `json:"latency_buckets_ms"`
}

type routeKey struct{ method, pattern string }

type registry struct {
	mu       sync.Mutex
	total    int64
	totalMs  int64
	byStatus map[int]int64
	routes   map[routeKey]*routeMetrics
}

type routeMetrics struct {
	count        int64
	errors       int64
	authFailures int64
	totalMs      int64
	histogram    []int64
}

// defaultRegistry records every request through the shared middleware chain.
var defaultRegistry = &registry{
	byStatus: make(map[int]int64),
	routes:   make(map[routeKey]*routeMetrics),
}

// Report returns a copy of the aggregated request metrics.
func Report() MetricsReport {
	reg := defaultRegistry

	reg.mu.Lock()
	total, totalMs := reg.total, reg.totalMs
	byStatus := make(map[string]int64, len(reg.byStatus))
	for s, n := range reg.byStatus {
		byStatus[statusName(s)] = n
	}
	routes := make([]RouteMetrics, 0, len(reg.routes))
	var authFailures int64
	for k, acc := range reg.routes {
		rm := RouteMetrics{
			Method:       k.method,
			Pattern:      k.pattern,
			Count:        acc.count,
			Errors:       acc.errors,
			AuthFailures: acc.authFailures,
			TotalMs:      acc.totalMs,
			Histogram:    append([]int64(nil), acc.histogram...),
		}
		authFailures += rm.AuthFailures
		routes = append(routes, rm)
	}
	reg.mu.Unlock()

	sort.Slice(routes, func(i, j int) bool {
		if routes[i].Pattern != routes[j].Pattern {
			return routes[i].Pattern < routes[j].Pattern
		}
		return routes[i].Method < routes[j].Method
	})

	return MetricsReport{
		TotalRequests:   total,
		TotalDurationMs: totalMs,
		AuthFailures:    authFailures,
		ByStatus:        byStatus,
		Routes:          routes,
		LatencyBuckets:  append([]int64(nil), latencyBucketsMs...),
	}
}

// record is called by the shared logging middleware once per completed request.
func (reg *registry) record(method, pattern string, status int, dur time.Duration) {
	reg.mu.Lock()
	defer reg.mu.Unlock()

	reg.total++
	reg.totalMs += dur.Milliseconds()
	reg.byStatus[status]++

	key := routeKey{method: method, pattern: pattern}
	acc := reg.routes[key]
	if acc == nil {
		acc = &routeMetrics{histogram: make([]int64, len(latencyBucketsMs)+1)}
		reg.routes[key] = acc
	}
	acc.count++
	acc.totalMs += dur.Milliseconds()
	if status >= 500 {
		acc.errors++
	}
	if status == http.StatusUnauthorized || status == http.StatusForbidden {
		acc.authFailures++
	}
	acc.histogram[histogramBin(dur.Milliseconds())]++
}

func histogramBin(ms int64) int {
	for i, bound := range latencyBucketsMs {
		if ms <= bound {
			return i
		}
	}
	return len(latencyBucketsMs) // overflow bucket
}

func statusName(s int) string {
	switch {
	case s >= 500:
		return "5xx"
	case s >= 400:
		return "4xx"
	case s >= 300:
		return "3xx"
	case s >= 200:
		return "2xx"
	default:
		return "other"
	}
}

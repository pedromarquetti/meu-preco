package nfce

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"sync/atomic"
	"testing"
	"time"
)

func TestRateLimiterQueueBudgetFailsFast(t *testing.T) {
	// One slot every 10s, but a caller may wait at most 1s.
	rl := NewPortalLimiterWithMaxWait(1, 10*time.Second, time.Second)
	if err := rl.Wait(context.Background()); err != nil {
		t.Fatal(err)
	}

	start := time.Now()
	err := rl.Wait(context.Background())
	elapsed := time.Since(start)

	var busy *PortalBusyError
	if !errors.As(err, &busy) {
		t.Fatalf("Wait error = %v, want *PortalBusyError", err)
	}
	if !errors.Is(err, ErrPortalBusy) {
		t.Errorf("error = %v, want ErrPortalBusy", err)
	}
	if elapsed > 250*time.Millisecond {
		t.Errorf("Wait took %v, want an immediate busy error", elapsed)
	}
	if busy.RetryAfter < 9*time.Second {
		t.Errorf("RetryAfter = %v, want ~10s (the next slot)", busy.RetryAfter)
	}
}

func TestRateLimiterRejectedWaitDoesNotReserveSlot(t *testing.T) {
	rl := NewPortalLimiterWithMaxWait(1, 10*time.Second, time.Second)
	if err := rl.Wait(context.Background()); err != nil {
		t.Fatal(err)
	}

	rl.mu.Lock()
	nextBefore := rl.next
	rl.mu.Unlock()

	var first *PortalBusyError
	if err := rl.Wait(context.Background()); !errors.As(err, &first) {
		t.Fatalf("first rejected Wait error = %v, want *PortalBusyError", err)
	}

	rl.mu.Lock()
	nextAfter := rl.next
	rl.mu.Unlock()
	if !nextAfter.Equal(nextBefore) {
		t.Errorf("queue advanced on a rejected request: next %v -> %v", nextBefore, nextAfter)
	}

	// A third caller sees the same queue position, not one inflated by the
	// rejected request.
	var second *PortalBusyError
	if err := rl.Wait(context.Background()); !errors.As(err, &second) {
		t.Fatalf("second rejected Wait error = %v, want *PortalBusyError", err)
	}
	if delta := second.RetryAfter - first.RetryAfter; delta < -50*time.Millisecond || delta > 50*time.Millisecond {
		t.Errorf("RetryAfter drifted from %v to %v after a rejected request", first.RetryAfter, second.RetryAfter)
	}
}

func TestRateLimiterZeroQueueMaxWaitRejectsQueueing(t *testing.T) {
	rl := NewPortalLimiterWithMaxWait(10, 0, 0) // 100ms between slots, no queue budget
	if err := rl.Wait(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := rl.Wait(context.Background()); !errors.Is(err, ErrPortalBusy) {
		t.Fatalf("Wait error = %v, want ErrPortalBusy", err)
	}
}

func TestLimiterConstructionUsesConfiguredQueueBudget(t *testing.T) {
	src, err := NewHTTPSource(HTTPSourceOptions{BaseURL: "https://portal.example"})
	if err != nil {
		t.Fatal(err)
	}
	if src.limit.maxWait != DefaultPortalQueueMaxWait {
		t.Errorf("default source maxWait = %v, want %v", src.limit.maxWait, DefaultPortalQueueMaxWait)
	}

	src, err = NewHTTPSource(HTTPSourceOptions{BaseURL: "https://portal.example", QueueMaxWait: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	if src.limit.maxWait != time.Second {
		t.Errorf("configured source maxWait = %v, want 1s", src.limit.maxWait)
	}

	solver := NewHTTPChallengeSolver(HTTPChallengeSolverOptions{})
	if solver.limit.maxWait != DefaultPortalQueueMaxWait {
		t.Errorf("default solver maxWait = %v, want %v", solver.limit.maxWait, DefaultPortalQueueMaxWait)
	}

	solver = NewHTTPChallengeSolver(HTTPChallengeSolverOptions{QueueMaxWait: time.Second})
	if solver.limit.maxWait != time.Second {
		t.Errorf("configured solver maxWait = %v, want 1s", solver.limit.maxWait)
	}
}

func TestHTTPSourceQueueBudgetDoesNotContactPortal(t *testing.T) {
	raw, err := os.ReadFile("../../testdata/nfce_1.html")
	if err != nil {
		t.Fatal(err)
	}

	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		_, _ = w.Write(raw)
	}))
	defer srv.Close()

	limit := NewPortalLimiterWithMaxWait(1, 10*time.Second, time.Second)
	src, err := NewHTTPSource(HTTPSourceOptions{
		BaseURL:    srv.URL,
		Limiter:    limit,
		MaxRetries: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	key, _ := ParseAccessKey(senateKey)

	if _, err := src.Fetch(context.Background(), key, ""); err != nil {
		t.Fatalf("first fetch: %v", err)
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("portal calls = %d, want 1 after the first fetch", got)
	}

	start := time.Now()
	_, err = src.Fetch(context.Background(), key, "")
	elapsed := time.Since(start)

	var busy *PortalBusyError
	if !errors.As(err, &busy) {
		t.Fatalf("second fetch error = %v, want *PortalBusyError", err)
	}
	if elapsed > 250*time.Millisecond {
		t.Errorf("second fetch took %v, want an immediate busy error", elapsed)
	}
	if got := calls.Load(); got != 1 {
		t.Errorf("portal calls = %d, want 1: a rejected queue wait must not reach the portal", got)
	}
}

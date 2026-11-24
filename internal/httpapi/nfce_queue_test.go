package httpapi

import (
	"context"
	"net/http"
	"strconv"
	"testing"
	"time"

	"meupreco/internal/domain"
	"meupreco/internal/nfce"
)

// queueResolver consumes a real portal-limiter slot before resolving, like the
// production resolver: whether a draft waits or answers busy is decided by the
// limiter's queue budget together with the handler's request budget.
type queueResolver struct{ limit *nfce.PortalLimiter }

func (r queueResolver) Resolve(ctx context.Context, _ nfce.AccessKey, _ string) (*domain.Nfce, error) {
	if err := r.limit.Wait(ctx); err != nil {
		return nil, err
	}
	return nil, nfce.ErrUpstream
}

// TestNfceNewQueueSaturationReturnsBusyBeforeWriteTimeout proves that a
// saturated pacing queue produces a fast, retryable 503 instead of holding the
// request until the client (or the server write deadline) gives up.
func TestNfceNewQueueSaturationReturnsBusyBeforeWriteTimeout(t *testing.T) {
	limit := nfce.NewPortalLimiterWithMaxWait(1, 10*time.Second, time.Second)
	if err := limit.Wait(context.Background()); err != nil { // occupy the only slot
		t.Fatal(err)
	}

	handler := nfceRouter(t, queueResolver{limit: limit}, nil)

	start := time.Now()
	rec := postNfce(t, handler, "/api/v1/nfce/new", `{"access_key":"`+senateKey+`"}`)
	elapsed := time.Since(start)

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503 (body %s)", rec.Code, rec.Body.String())
	}
	if code, _ := decodeErrorEnvelope(t, rec); code != "upstream_busy" {
		t.Errorf("error code = %q, want upstream_busy", code)
	}

	retry, err := strconv.Atoi(rec.Header().Get("Retry-After"))
	if err != nil || retry < 9 || retry > 10 {
		t.Errorf("Retry-After = %q (err %v), want whole seconds up to the next slot (~10)", rec.Header().Get("Retry-After"), err)
	}

	if elapsed >= time.Second {
		t.Errorf("handler took %v, want an immediate retryable answer well before the 30s write timeout", elapsed)
	}
}

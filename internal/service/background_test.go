package service

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"
	"time"

	"meupreco/internal/domain"
	"meupreco/internal/enrich"
	"meupreco/internal/product"
)

func discardLog() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func TestPurgeExpiredRemovesStaleOnly(t *testing.T) {
	m := newMemStores()
	now := time.Now()

	fresh := &domain.Preview{ID: 1, UserID: 1, AccessKey: "fresh", Status: "pending", TTL: now.Add(time.Hour)}
	stale := &domain.Preview{ID: 2, UserID: 1, AccessKey: "stale", Status: "pending", TTL: now.Add(-time.Minute)}
	m.previews[fresh.ID] = fresh
	m.previews[stale.ID] = stale
	m.previewKey[previewKeyFor(1, "fresh")] = fresh.ID
	m.previewKey[previewKeyFor(1, "stale")] = stale.ID

	if err := purgeExpired(context.Background(), discardLog(), memPreviews{m}); err != nil {
		t.Fatalf("purge: %v", err)
	}
	if _, ok := m.previews[stale.ID]; ok {
		t.Error("stale preview not purged")
	}
	if _, ok := m.previews[fresh.ID]; !ok {
		t.Error("fresh preview was purged")
	}
}

func TestRunPreviewGCLifecycle(t *testing.T) {
	m := newMemStores()
	stale := &domain.Preview{ID: 1, UserID: 1, AccessKey: "stale", Status: "pending", TTL: time.Now().Add(-time.Minute)}
	m.previews[stale.ID] = stale
	m.previewKey[previewKeyFor(1, "stale")] = stale.ID

	ctx, cancel := context.WithCancel(context.Background())

	done := make(chan error, 1)
	go func() {
		done <- RunPreviewGC(ctx, discardLog(), memPreviews{m}, 5*time.Millisecond)
	}()

	time.Sleep(30 * time.Millisecond)
	cancel()

	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("RunPreviewGC = %v, want context.Canceled", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("RunPreviewGC did not stop after cancel")
	}

	if _, ok := m.previews[stale.ID]; ok {
		t.Error("stale preview survived the background sweep")
	}
}

type fakeEnricher struct {
	product *enrich.Product
	err     error
}

func (f fakeEnricher) Lookup(_ context.Context, gtin string) (*enrich.Product, error) {
	return f.product, f.err
}

func TestEnrichPlanAugmentsGtinNewOnly(t *testing.T) {
	svc := New(nil, nil, nil, nil)
	svc.Enricher = fakeEnricher{product: &enrich.Product{
		Name: "Arroz Integral 1kg", Brand: "Tio João", Unit: "KG",
	}}

	plan := []PlanItem{
		{Line: 1, Status: product.StatusGtinNew, GTIN: "07896008038717", ProductName: "ARROZ INTEGRAL", Message: ""},
		{Line: 2, Status: product.StatusGtinKnown, GTIN: "07896008038717", ProductName: "known", Message: ""},
		{Line: 3, Status: product.StatusGtinNew, GTIN: "", ProductName: "no barcode", Message: ""},
	}
	svc.enrichPlan(context.Background(), plan)

	if plan[0].ProductName != "Arroz Integral 1kg" {
		t.Errorf("enriched name = %q", plan[0].ProductName)
	}
	if plan[0].Brand != "Tio João" {
		t.Errorf("enriched brand = %q", plan[0].Brand)
	}
	if plan[0].Message == "" {
		t.Error("enriched line carries no message")
	}
	if plan[1].ProductName != "known" {
		t.Errorf("gtin_known line was touched: %q", plan[1].ProductName)
	}
	if plan[2].ProductName != "no barcode" {
		t.Errorf("empty-gtin line was touched: %q", plan[2].ProductName)
	}
}

func TestEnrichPlanDegradesOffline(t *testing.T) {
	svc := New(nil, nil, nil, nil)
	svc.Enricher = fakeEnricher{err: enrich.ErrProductNotFound}

	plan := []PlanItem{{Line: 1, Status: product.StatusGtinNew, GTIN: "07896008038717", ProductName: "ARROZ"}}
	svc.enrichPlan(context.Background(), plan)
	if plan[0].ProductName != "ARROZ" {
		t.Errorf("offline enrichment changed the plan: %q", plan[0].ProductName)
	}
}

func TestEnrichPlanDisabled(t *testing.T) {
	svc := New(nil, nil, nil, nil)
	plan := []PlanItem{{Line: 1, Status: product.StatusGtinNew, GTIN: "07896008038717", ProductName: "ARROZ"}}
	svc.enrichPlan(context.Background(), plan)
	if plan[0].ProductName != "ARROZ" {
		t.Error("nil Enricher changed the plan")
	}
}

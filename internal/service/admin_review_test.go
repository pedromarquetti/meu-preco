package service

import (
	"context"
	"errors"
	"testing"

	"github.com/shopspring/decimal"

	"meupreco/internal/domain"
)

func reviewTestLog(t *testing.T, m *memStores, productID *int64, needsReview bool) *domain.PriceLog {
	t.Helper()
	log := &domain.PriceLog{
		NfceID: 1, Line: int(m.nextLogID + 1), ProductID: productID,
		DescriptionSnapshot: "LIMAO TAHITI KG", Qty: decimal.NewFromInt(1),
		Unit: "KG", UnitPrice: decimal.NewFromInt(5), TotalPrice: decimal.NewFromInt(5),
		NeedsReview: needsReview,
	}
	if err := m.PriceLogs().Create(context.Background(), log); err != nil {
		t.Fatal(err)
	}
	return log
}

func TestReviewPriceLogApproveAndReject(t *testing.T) {
	m := newMemStores()
	p := &domain.Product{ProductKey: "lime", Name: "Limao", Unit: "KG"}
	if _, err := m.Products().Create(context.Background(), p); err != nil {
		t.Fatal(err)
	}
	productID := p.ID
	log := reviewTestLog(t, m, &productID, true)
	svc := newTestService(m)

	if _, err := svc.ReviewPriceLog(context.Background(), 9, log.ID, "reject"); err != nil {
		t.Fatalf("reject: %v", err)
	}
	if !log.NeedsReview || len(m.priceLogEvents) != 1 || m.priceLogEvents[0].Action != "reject" {
		t.Fatalf("rejection did not preserve pending state: log=%+v events=%+v", log, m.priceLogEvents)
	}

	if _, err := svc.ReviewPriceLog(context.Background(), 9, log.ID, "approve"); err != nil {
		t.Fatalf("approve after reject: %v", err)
	}
	if log.NeedsReview || len(m.priceLogEvents) != 2 || m.priceLogEvents[1].Action != "approve" {
		t.Fatalf("approval did not publish and append event: log=%+v events=%+v", log, m.priceLogEvents)
	}
}

func TestReviewPriceLogApprovalRequiresProductAndIsAtomic(t *testing.T) {
	m := newMemStores()
	log := reviewTestLog(t, m, nil, true)
	svc := newTestService(m)

	if _, err := svc.ReviewPriceLog(context.Background(), 9, log.ID, "approve"); !errors.Is(err, ErrMissingProductForApproval) {
		t.Fatalf("error = %v, want ErrMissingProductForApproval", err)
	}
	if !log.NeedsReview || len(m.priceLogEvents) != 0 {
		t.Fatalf("failed approval changed state: log=%+v events=%d", log, len(m.priceLogEvents))
	}

	if _, err := svc.ReviewPriceLog(context.Background(), 9, log.ID, "reject"); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.ReviewPriceLog(context.Background(), 9, log.ID, "reject"); !errors.Is(err, ErrPriceLogAlreadyProcessed) {
		t.Fatalf("repeated rejection error = %v, want ErrPriceLogAlreadyProcessed", err)
	}
	if len(m.priceLogEvents) != 1 {
		t.Fatalf("repeated rejection created an event: %d", len(m.priceLogEvents))
	}
	if _, err := svc.ReviewPriceLog(context.Background(), 9, log.ID, "bad"); !errors.Is(err, ErrInvalidReviewAction) {
		t.Fatalf("error = %v, want ErrInvalidReviewAction", err)
	}
}

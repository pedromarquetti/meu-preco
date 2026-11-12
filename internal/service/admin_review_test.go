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

func TestProposalStatus(t *testing.T) {
	assign := ActionAssign
	approve := ActionApprove
	reject := ActionReject

	tests := []struct {
		name   string
		action *string
		want   string
	}{
		{name: "no event", want: ProposalStatusPending},
		{name: "empty action", action: strPtr(""), want: ProposalStatusPending},
		{name: "assign", action: &assign, want: ProposalStatusPending},
		{name: "approve", action: &approve, want: ProposalStatusPending},
		{name: "reject", action: &reject, want: ProposalStatusRejected},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := proposalStatus(tt.action); got != tt.want {
				t.Errorf("proposalStatus(%v) = %q, want %q", tt.action, got, tt.want)
			}
		})
	}
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

func TestReviewPriceLogRequiresProductAndIsAtomic(t *testing.T) {
	m := newMemStores()
	log := reviewTestLog(t, m, nil, true)
	svc := newTestService(m)

	if _, err := svc.ReviewPriceLog(context.Background(), 9, log.ID, "approve"); !errors.Is(err, ErrMissingProductForApproval) {
		t.Fatalf("approve error = %v, want ErrMissingProductForApproval", err)
	}
	if _, err := svc.ReviewPriceLog(context.Background(), 9, log.ID, "reject"); !errors.Is(err, ErrMissingProductForRejection) {
		t.Fatalf("reject error = %v, want ErrMissingProductForRejection", err)
	}
	if !log.NeedsReview || len(m.priceLogEvents) != 0 {
		t.Fatalf("failed review changed state: log=%+v events=%d", log, len(m.priceLogEvents))
	}

	if _, err := svc.ReviewPriceLog(context.Background(), 9, log.ID, "bad"); !errors.Is(err, ErrInvalidReviewAction) {
		t.Fatalf("error = %v, want ErrInvalidReviewAction", err)
	}

	p := &domain.Product{ProductKey: "lime", Name: "Limao", Unit: "KG"}
	if _, err := m.Products().Create(context.Background(), p); err != nil {
		t.Fatal(err)
	}
	if err := m.PriceLogs().SetProductProposal(context.Background(), log.ID, p.ID, 9, ActionAssign); err != nil {
		t.Fatal(err)
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
}

// TestNeedsReviewQueueProposalStates covers the queue projection for every
// proposal state: unresolved without events, assigned, rejected and approved.
func TestNeedsReviewQueueProposalStates(t *testing.T) {
	newProduct := func(t *testing.T, m *memStores) *domain.Product {
		t.Helper()
		p := &domain.Product{ProductKey: "lime", Name: "Limao", Unit: "KG"}
		if _, err := m.Products().Create(context.Background(), p); err != nil {
			t.Fatal(err)
		}
		return p
	}
	queue := func(t *testing.T, svc *Service) []ReviewItemView {
		t.Helper()
		q, err := svc.NeedsReviewQueue(context.Background(), 1, 20)
		if err != nil {
			t.Fatalf("queue: %v", err)
		}
		return q.Items
	}

	t.Run("unresolved without event", func(t *testing.T) {
		m := newMemStores()
		reviewTestLog(t, m, nil, true)
		svc := newTestService(m)

		items := queue(t, svc)
		if len(items) != 1 {
			t.Fatalf("queue = %+v, want one unresolved row", items)
		}
		it := items[0]
		if it.ProposalStatus != ProposalStatusPending || it.LastReviewAction != nil ||
			it.ProposedProduct != nil || !it.NeedsReview {
			t.Errorf("unresolved item = %+v, want pending/no action/no product", it)
		}
	})

	t.Run("assigned proposal", func(t *testing.T) {
		m := newMemStores()
		p := newProduct(t, m)
		log := reviewTestLog(t, m, &p.ID, true)
		svc := newTestService(m)

		if _, err := svc.BatchAssign(context.Background(), 9, BatchAssignRequest{
			Assignments: []Assignment{{ID: log.ID, Action: "existing", ProductID: p.ID}},
		}); err != nil {
			t.Fatalf("assign: %v", err)
		}
		items := queue(t, svc)
		if len(items) != 1 {
			t.Fatalf("queue = %+v, want one assigned proposal", items)
		}
		it := items[0]
		if it.ProposalStatus != ProposalStatusPending || it.LastReviewAction == nil ||
			*it.LastReviewAction != ActionAssign {
			t.Errorf("assigned item = %+v, want pending/assign", it)
		}
		if it.ProposedProduct == nil || it.ProposedProduct.ID != p.ID {
			t.Errorf("proposed_product = %+v, want %d", it.ProposedProduct, p.ID)
		}
	})

	t.Run("rejected proposal", func(t *testing.T) {
		m := newMemStores()
		p := newProduct(t, m)
		log := reviewTestLog(t, m, &p.ID, true)
		svc := newTestService(m)

		if _, err := svc.BatchAssign(context.Background(), 9, BatchAssignRequest{
			Assignments: []Assignment{{ID: log.ID, Action: "existing", ProductID: p.ID}},
		}); err != nil {
			t.Fatalf("assign: %v", err)
		}
		if _, err := svc.ReviewPriceLog(context.Background(), 9, log.ID, ActionReject); err != nil {
			t.Fatalf("reject: %v", err)
		}
		items := queue(t, svc)
		if len(items) != 1 {
			t.Fatalf("queue = %+v, want the rejected proposal still visible", items)
		}
		it := items[0]
		if it.ProposalStatus != ProposalStatusRejected || it.LastReviewAction == nil ||
			*it.LastReviewAction != ActionReject {
			t.Errorf("rejected item = %+v, want rejected/reject", it)
		}
		if it.ProposedProduct == nil || it.ProposedProduct.ID != p.ID || !it.NeedsReview {
			t.Errorf("rejected item lost its proposal: %+v", it)
		}
	})

	t.Run("approved proposal leaves the queue", func(t *testing.T) {
		m := newMemStores()
		p := newProduct(t, m)
		log := reviewTestLog(t, m, &p.ID, true)
		svc := newTestService(m)

		if _, err := svc.BatchAssign(context.Background(), 9, BatchAssignRequest{
			Assignments: []Assignment{{ID: log.ID, Action: "existing", ProductID: p.ID}},
		}); err != nil {
			t.Fatalf("assign: %v", err)
		}
		if _, err := svc.ReviewPriceLog(context.Background(), 9, log.ID, ActionApprove); err != nil {
			t.Fatalf("approve: %v", err)
		}
		if items := queue(t, svc); len(items) != 0 {
			t.Errorf("approved row still in queue: %+v", items)
		}
		latest, err := m.Events().LatestByPriceLogID(context.Background(), log.ID)
		if err != nil {
			t.Fatalf("latest event: %v", err)
		}
		if latest.Action != ActionApprove || log.NeedsReview {
			t.Errorf("approval state = action %q needs_review %v, want approve/false", latest.Action, log.NeedsReview)
		}
	})
}

// TestReviewPriceLogApprovePublishesAndKeepsProvenance checks that approving a
// valid proposal clears needs_review, appends the approve event and leaves the
// original assignment's resolved_by/resolution_note untouched.
func TestReviewPriceLogApprovePublishesAndKeepsProvenance(t *testing.T) {
	m := newMemStores()
	p := &domain.Product{ProductKey: "lime", Name: "Limao", Unit: "KG"}
	if _, err := m.Products().Create(context.Background(), p); err != nil {
		t.Fatal(err)
	}
	productID := p.ID
	log := reviewTestLog(t, m, &productID, true)
	resolvedBy, note := int64(7), ActionAssign
	log.ResolvedBy, log.ResolutionNote = &resolvedBy, &note
	svc := newTestService(m)

	if _, err := svc.ReviewPriceLog(context.Background(), 9, log.ID, ActionApprove); err != nil {
		t.Fatalf("approve: %v", err)
	}
	if log.NeedsReview {
		t.Error("approval did not clear needs_review")
	}
	if log.ResolvedBy == nil || *log.ResolvedBy != resolvedBy ||
		log.ResolutionNote == nil || *log.ResolutionNote != note {
		t.Errorf("approval changed assignment provenance: resolved_by=%v note=%v", log.ResolvedBy, log.ResolutionNote)
	}
	if len(m.priceLogEvents) != 1 {
		t.Fatalf("events = %d, want one approve event", len(m.priceLogEvents))
	}
	ev := m.priceLogEvents[0]
	if ev.Action != ActionApprove || ev.UserID != 9 ||
		ev.FromProductID == nil || *ev.FromProductID != p.ID ||
		ev.ToProductID == nil || *ev.ToProductID != p.ID {
		t.Errorf("approve event = %+v, want approve %d->%d by user 9", ev, p.ID, p.ID)
	}

	if _, err := svc.ReviewPriceLog(context.Background(), 9, log.ID, ActionApprove); !errors.Is(err, ErrPriceLogAlreadyProcessed) {
		t.Fatalf("second approval error = %v, want ErrPriceLogAlreadyProcessed", err)
	}
}

// TestReviewPriceLogReassignmentResetsDecisionState verifies that a new assign
// event after a rejection lets the admin decide again: reject -> assign ->
// reject and reject -> assign -> approve must both be accepted.
func TestReviewPriceLogReassignmentResetsDecisionState(t *testing.T) {
	tests := []struct {
		name  string
		final string
	}{
		{name: "reassign then reject", final: ActionReject},
		{name: "reassign then approve", final: ActionApprove},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := newMemStores()
			a := &domain.Product{ProductKey: "banana-a", Name: "BANANA A", Unit: "KG"}
			b := &domain.Product{ProductKey: "banana-b", Name: "BANANA B", Unit: "KG"}
			for _, p := range []*domain.Product{a, b} {
				if _, err := m.Products().Create(context.Background(), p); err != nil {
					t.Fatal(err)
				}
			}
			productID := a.ID
			log := reviewTestLog(t, m, &productID, true)
			svc := newTestService(m)

			assign := func(productID int64) {
				t.Helper()
				if _, err := svc.BatchAssign(context.Background(), 9, BatchAssignRequest{
					Assignments: []Assignment{{ID: log.ID, Action: "existing", ProductID: productID}},
				}); err != nil {
					t.Fatalf("assign %d: %v", productID, err)
				}
			}

			assign(a.ID)
			if _, err := svc.ReviewPriceLog(context.Background(), 9, log.ID, ActionReject); err != nil {
				t.Fatalf("reject A: %v", err)
			}
			assign(b.ID)

			if _, err := svc.ReviewPriceLog(context.Background(), 9, log.ID, tt.final); err != nil {
				t.Fatalf("%s after reassignment: %v", tt.final, err)
			}
			latest, err := m.Events().LatestByPriceLogID(context.Background(), log.ID)
			if err != nil {
				t.Fatalf("latest event: %v", err)
			}
			if latest.Action != tt.final {
				t.Errorf("latest action = %q, want %q", latest.Action, tt.final)
			}
			if tt.final == ActionApprove {
				if log.NeedsReview {
					t.Error("approval after reassignment did not publish the row")
				}
			} else {
				if !log.NeedsReview {
					t.Error("rejection after reassignment removed the row from review")
				}
			}
			q, err := svc.NeedsReviewQueue(context.Background(), 1, 20)
			if err != nil {
				t.Fatalf("queue: %v", err)
			}
			if tt.final == ActionApprove {
				if len(q.Items) != 0 {
					t.Errorf("approved row still in queue: %+v", q.Items)
				}
			} else {
				if len(q.Items) != 1 || q.Items[0].ProposalStatus != ProposalStatusRejected {
					t.Errorf("rejected proposal queue = %+v, want one rejected item", q.Items)
				}
			}
		})
	}
}

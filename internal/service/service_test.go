package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/shopspring/decimal"

	"meupreco/internal/domain"
	"meupreco/internal/nfce"
	"meupreco/internal/product"
)

// senateKey is a real, publicly-disclosed NFC-e key from the Senate CEAPS
// transparency portal (SP restaurant receipt) — used only to exercise the
// validation path; no private data.
const testAccessKey = "35241152323688000144650020000002571115675901"

type stubResolver struct {
	receipt *domain.Nfce
	err     error
}

func (r stubResolver) Resolve(_ context.Context, _ nfce.AccessKey, _ string) (*domain.Nfce, error) {
	return r.receipt, r.err
}

// testReceipt mixes a known barcode (line 1 → gtin_known) with an
// unidentifiable commodity line (line 2 → uid_uncertain).
func testReceipt() *domain.Nfce {
	return &domain.Nfce{
		AccessKey:     testAccessKey,
		Supermarket:   domain.Supermarket{CNPJ: "12345678000199", LegalName: "MERCADO BOM PRECO LTDA"},
		Number:        "257",
		Series:        "002",
		Protocol:      "135241234567890",
		Environment:   "Producao",
		PaymentMethod: "Cartao de Credito",
		Total:         decimal.RequireFromString("59.06"),
		Items: []domain.NfceItem{
			{
				Line: 1, Code: "7891000412855", Description: "ACHOCOLATADO EM PO NESCAU 350G",
				Qty: decimal.RequireFromString("1"), Unit: "UN",
				UnitPrice: decimal.RequireFromString("22.99"), TotalPrice: decimal.RequireFromString("22.99"),
			},
			{
				Line: 2, Code: "", Description: "LIMAO TAHITI KG",
				Qty: decimal.RequireFromString("0.815"), Unit: "KG",
				UnitPrice: decimal.RequireFromString("5.99"), TotalPrice: decimal.RequireFromString("4.88"),
			},
		},
	}
}

func newTestService(m *memStores) *Service {
	svc := New(m, stubResolver{receipt: testReceipt()}, nil, nil)
	return svc
}

func previewForTest(t *testing.T, svc *Service, m *memStores, userID int64) *PreviewResult {
	t.Helper()
	res, err := svc.ReportPreview(context.Background(), PreviewRequest{
		UserID:    userID,
		AccessKey: testAccessKey,
	})
	if err != nil {
		t.Fatalf("preview: %v", err)
	}
	return res
}

func TestReportPreviewBuildsPlan(t *testing.T) {
	m := newMemStores()
	m.seedKnownProduct()
	svc := newTestService(m)

	res := previewForTest(t, svc, m, 1)

	if res.PreviewID == 0 {
		t.Fatal("preview_id not set")
	}
	if len(res.Plan) != 2 {
		t.Fatalf("plan items = %d, want 2", len(res.Plan))
	}

	l1 := res.Plan[0]
	if l1.Status != product.StatusGtinKnown || l1.GTIN != "07891000412855" {
		t.Errorf("line 1 = %+v, want gtin_known", l1)
	}
	if l1.Message == "" {
		t.Error("line 1 missing human message")
	}

	l2 := res.Plan[1]
	if l2.Status != product.StatusUIDUncertain {
		t.Errorf("line 2 status = %q, want uid_uncertain", l2.Status)
	}
	if l2.PricePerUnit != "5.98773" {
		t.Errorf("line 2 price_per_unit = %q, want 5.98773", l2.PricePerUnit)
	}

	if res.Totals.Items != 2 || res.Totals.AutoSave != 1 || res.Totals.UIDUncertain != 1 {
		t.Errorf("totals = %+v", res.Totals)
	}

	stored, err := m.Previews().GetPendingByUserAccessKey(context.Background(), 1, testAccessKey)
	if err != nil {
		t.Fatalf("preview not persisted: %v", err)
	}
	if stored.ID != res.PreviewID || stored.Status != "pending" {
		t.Errorf("stored preview = %+v", stored)
	}
}

func TestReportPreviewReplayIsIdempotent(t *testing.T) {
	m := newMemStores()
	m.seedKnownProduct()
	svc := newTestService(m)

	first := previewForTest(t, svc, m, 1)

	again, err := svc.ReportPreview(context.Background(), PreviewRequest{
		UserID:    1,
		AccessKey: testAccessKey,
	})
	if err != nil {
		t.Fatalf("replay: %v", err)
	}
	if again.PreviewID != first.PreviewID {
		t.Errorf("replay preview_id = %d, want %d", again.PreviewID, first.PreviewID)
	}
	if len(again.Plan) != 2 {
		t.Errorf("replay plan items = %d, want 2", len(again.Plan))
	}
}

func TestReportPreviewFreshDraftAfterCommit(t *testing.T) {
	m := newMemStores()
	m.seedKnownProduct()
	svc := newTestService(m)
	res := previewForTest(t, svc, m, 1)

	if err := m.Previews().MarkCommitted(context.Background(), res.PreviewID); err != nil {
		t.Fatalf("mark committed: %v", err)
	}

	// /nfce/new never 409s on a committed receipt: the partial unique index
	// covers only pending drafts, so a fresh plan is always produced and the
	// submit that follows is the silent no-op.
	again, err := svc.ReportPreview(context.Background(), PreviewRequest{
		UserID: 1, AccessKey: testAccessKey,
	})
	if err != nil {
		t.Fatalf("re-draft after commit: %v", err)
	}
	if again.PreviewID == res.PreviewID {
		t.Errorf("re-draft reused the committed preview (id %d)", again.PreviewID)
	}
	if again.Status != "pending" {
		t.Errorf("re-draft status = %q, want pending", again.Status)
	}
}

func TestReportPreviewReplacesExpiredKey(t *testing.T) {
	m := newMemStores()
	m.seedKnownProduct()
	svc := newTestService(m)
	res := previewForTest(t, svc, m, 1)

	p, _ := m.Previews().GetByID(context.Background(), res.PreviewID)
	p.TTL = time.Now().Add(-time.Minute) // force expiry under the shared clock

	again, err := svc.ReportPreview(context.Background(), PreviewRequest{
		UserID: 1, AccessKey: testAccessKey,
	})
	if err != nil {
		t.Fatalf("replace: %v", err)
	}
	if again.PreviewID == res.PreviewID {
		t.Errorf("expired preview was reused (id %d), want a fresh row", again.PreviewID)
	}
	if again.ExpiresAt.Before(time.Now()) {
		t.Errorf("fresh preview already expired: %v", again.ExpiresAt)
	}
}

// TestReportPreviewSkipsForeignKeyError: the (user, access_key) partial index
// is owner-scoped, so a second user drafting the same receipt always gets
// their own pending plan instead of a key-conflict.
func TestReportPreviewAllowsSecondUserDraft(t *testing.T) {
	m := newMemStores()
	m.seedKnownProduct()
	svc := newTestService(m)

	user1 := previewForTest(t, svc, m, 1)

	user2, err := svc.ReportPreview(context.Background(), PreviewRequest{
		UserID: 2, AccessKey: testAccessKey,
	})
	if err != nil {
		t.Fatalf("second user draft: %v", err)
	}
	if user2.PreviewID == user1.PreviewID {
		t.Errorf("users share a preview id %d, want independent drafts", user2.PreviewID)
	}
	if len(m.previews) != 2 {
		t.Errorf("previews = %d, want 2", len(m.previews))
	}
}

func TestCommitConfirmFalseWritesNothing(t *testing.T) {
	m := newMemStores()
	m.seedKnownProduct()
	svc := newTestService(m)
	res := previewForTest(t, svc, m, 1)

	out, err := svc.CommitPreview(context.Background(), CommitRequest{
		UserID: 1, PreviewID: res.PreviewID, Confirm: false,
	})
	if err != nil {
		t.Fatalf("commit dry-run: %v", err)
	}
	if out.Confirmed || out.Written != 0 {
		t.Errorf("dry run wrote: %+v", out)
	}
	if len(m.priceLogs) != 0 || len(m.nfces) != 0 {
		t.Error("dry run must not write shared rows")
	}
	p, _ := m.Previews().GetByID(context.Background(), res.PreviewID)
	if p.Status != "pending" {
		t.Errorf("dry run flipped status to %q", p.Status)
	}
}

// TestCommitDefaultsUnresolvedToNeedsReview: an unresolved uid_uncertain line
// (no resolutions array at all) is persisted as a private review row rather
// than silently skipped — skip is opt-in only.
func TestCommitDefaultsUnresolvedToNeedsReview(t *testing.T) {
	m := newMemStores()
	m.seedKnownProduct()
	svc := newTestService(m)
	res := previewForTest(t, svc, m, 1)

	out, err := svc.CommitPreview(context.Background(), CommitRequest{
		UserID: 1, PreviewID: res.PreviewID, Confirm: true,
	})
	if err != nil {
		t.Fatalf("commit: %v", err)
	}
	if out.Written != 2 || out.AutoSaved != 1 || out.Review != 1 || out.Skipped != 0 {
		t.Errorf("commit result = %+v, want 2 written (1 auto, 1 review), 0 skipped", out)
	}
	if out.NfceID == 0 {
		t.Error("nfce_id not set")
	}

	if len(m.priceLogs) != 2 {
		t.Fatalf("price_logs = %d, want 2", len(m.priceLogs))
	}

	var auto, review *domain.PriceLog
	for _, l := range m.priceLogs {
		switch l.Line {
		case 1:
			auto = l
		case 2:
			review = l
		}
	}
	if auto == nil || auto.ProductID == nil || auto.IdentityStatus != product.StatusGtinKnown || auto.NeedsReview {
		t.Errorf("line 1 log = %+v, want auto-saved gtin_known", auto)
	}
	if review == nil {
		t.Fatal("line 2 log missing")
	}
	if review.ProductID != nil {
		t.Errorf("review log product = %v, want nil", review.ProductID)
	}
	if !review.NeedsReview {
		t.Error("unresolved uid_uncertain line not flagged needs_review")
	}
	if review.IdentityStatus != product.StatusUIDUncertain {
		t.Errorf("identity_status = %q, want uid_uncertain", review.IdentityStatus)
	}
	if review.ResolutionNote == nil || *review.ResolutionNote != ActionNeedsReview {
		t.Errorf("resolution_note = %v, want needs_review", review.ResolutionNote)
	}
	if len(m.products) != 1 {
		t.Errorf("needs_review created a product: %d rows", len(m.products))
	}

	if len(m.nfces) != 1 {
		t.Fatalf("nfces = %d, want 1", len(m.nfces))
	}
	if len(m.supermarkets) != 1 {
		t.Fatalf("supermarkets = %d, want 1", len(m.supermarkets))
	}

	p, _ := m.Previews().GetByID(context.Background(), res.PreviewID)
	if p.Status != "committed" {
		t.Errorf("status = %q, want committed", p.Status)
	}
}

// TestCommitExplicitSkipWritesNothing: skip remains the only way to discard a
// uid_uncertain line, and it writes no row.
func TestCommitExplicitSkipWritesNothing(t *testing.T) {
	m := newMemStores()
	m.seedKnownProduct()
	svc := newTestService(m)
	res := previewForTest(t, svc, m, 1)

	out, err := svc.CommitPreview(context.Background(), CommitRequest{
		UserID: 1, PreviewID: res.PreviewID, Confirm: true,
		Resolutions: []Resolution{{Line: 2, Action: ActionSkip}},
	})
	if err != nil {
		t.Fatalf("commit: %v", err)
	}
	if out.Written != 1 || out.AutoSaved != 1 || out.Skipped != 1 || out.Review != 0 {
		t.Errorf("commit result = %+v, want 1 auto-written / 1 skipped / 0 review", out)
	}
	for _, l := range m.priceLogs {
		if l.Line == 2 {
			t.Errorf("explicit skip wrote a line-2 row: %+v", l)
		}
	}
}

// TestCommittedReviewRowCanBeAssigned: a review row persisted by an unresolved
// commit is surfaced in the admin queue and can be resolved through the
// existing batch-assign path, clearing needs_review and recording the resolver.
func TestCommittedReviewRowCanBeAssigned(t *testing.T) {
	m := newMemStores()
	m.seedKnownProduct()
	admin := &domain.User{Email: "admin@example.com", Role: "admin"}
	if err := m.Users().Create(context.Background(), admin); err != nil {
		t.Fatalf("create admin: %v", err)
	}
	svc := newTestService(m)
	res := previewForTest(t, svc, m, 1)

	if _, err := svc.CommitPreview(context.Background(), CommitRequest{
		UserID: 1, PreviewID: res.PreviewID, Confirm: true,
	}); err != nil {
		t.Fatalf("commit: %v", err)
	}

	target := &domain.Product{ProductKey: "limao", Name: "LIMÃO TAHITI", Unit: "KG"}
	if err := m.Products().Create(context.Background(), target); err != nil {
		t.Fatalf("seed target: %v", err)
	}

	q, err := svc.NeedsReviewQueue(context.Background(), 1, 20)
	if err != nil {
		t.Fatalf("queue: %v", err)
	}
	if len(q.Items) != 1 || q.Items[0].Line != 2 {
		t.Fatalf("review queue = %+v, want the line-2 review row", q.Items)
	}

	if _, err := svc.BatchAssign(context.Background(), admin.ID, BatchAssignRequest{
		Assignments: []Assignment{{ID: q.Items[0].PriceLogID, Action: "existing", ProductID: target.ID}},
	}); err != nil {
		t.Fatalf("batch assign: %v", err)
	}

	var assigned *domain.PriceLog
	for _, l := range m.priceLogs {
		if l.Line == 2 {
			assigned = l
		}
	}
	if assigned == nil || assigned.ProductID == nil || *assigned.ProductID != target.ID {
		t.Fatalf("line 2 = %+v, want product %d", assigned, target.ID)
	}
	if assigned.NeedsReview {
		t.Error("assigned review row still flagged needs_review")
	}
	if assigned.ResolvedBy == nil || *assigned.ResolvedBy != admin.ID {
		t.Errorf("resolved_by = %v, want admin %d", assigned.ResolvedBy, admin.ID)
	}

	after, err := svc.NeedsReviewQueue(context.Background(), 1, 20)
	if err != nil {
		t.Fatalf("queue after assign: %v", err)
	}
	if len(after.Items) != 0 {
		t.Errorf("review queue still has %d items after assignment", len(after.Items))
	}
}

func TestCommitAssignResolution(t *testing.T) {
	m := newMemStores()
	m.seedKnownProduct()
	svc := newTestService(m)
	res := previewForTest(t, svc, m, 1)

	target := &domain.Product{ProductKey: "limao", Name: "LIMÃO TAHITI", Unit: "KG"}
	if err := m.Products().Create(context.Background(), target); err != nil {
		t.Fatalf("seed target: %v", err)
	}

	out, err := svc.CommitPreview(context.Background(), CommitRequest{
		UserID: 1, PreviewID: res.PreviewID, Confirm: true,
		Resolutions: []Resolution{{Line: 2, Action: ActionAssign, ProductID: target.ID}},
	})
	if err != nil {
		t.Fatalf("commit: %v", err)
	}
	if out.Written != 2 || out.Assigned != 1 {
		t.Errorf("commit result = %+v, want 2 written, 1 assigned", out)
	}

	var assigned *domain.PriceLog
	for _, l := range m.priceLogs {
		if l.Line == 2 {
			assigned = l
		}
	}
	if assigned == nil {
		t.Fatal("line 2 log missing")
	}
	if assigned.ProductID == nil || *assigned.ProductID != target.ID {
		t.Errorf("assign product = %v, want %d", assigned.ProductID, target.ID)
	}
	if assigned.ResolvedBy == nil || *assigned.ResolvedBy != 1 {
		t.Errorf("resolved_by = %v, want user 1", assigned.ResolvedBy)
	}
}

func TestCommitAssignMissingProductRejected(t *testing.T) {
	m := newMemStores()
	m.seedKnownProduct()
	svc := newTestService(m)
	res := previewForTest(t, svc, m, 1)

	_, err := svc.CommitPreview(context.Background(), CommitRequest{
		UserID: 1, PreviewID: res.PreviewID, Confirm: true,
		Resolutions: []Resolution{{Line: 2, Action: ActionAssign, ProductID: 999}},
	})
	if !errors.Is(err, ErrInvalidResolution) {
		t.Fatalf("err = %v, want ErrInvalidResolution", err)
	}
	if len(m.priceLogs) != 0 {
		t.Errorf("failed commit wrote %d rows", len(m.priceLogs))
	}
}

func TestCommitNeedsReviewKeepsPrivate(t *testing.T) {
	m := newMemStores()
	m.seedKnownProduct()
	svc := newTestService(m)
	res := previewForTest(t, svc, m, 1)

	out, err := svc.CommitPreview(context.Background(), CommitRequest{
		UserID: 1, PreviewID: res.PreviewID, Confirm: true,
		Resolutions: []Resolution{{Line: 2, Action: ActionNeedsReview}},
	})
	if err != nil {
		t.Fatalf("commit: %v", err)
	}
	if out.Review != 1 {
		t.Errorf("commit result = %+v, want 1 in review", out)
	}

	var review *domain.PriceLog
	for _, l := range m.priceLogs {
		if l.Line == 2 {
			review = l
		}
	}
	if review == nil {
		t.Fatal("line 2 log missing")
	}
	if review.ProductID != nil {
		t.Errorf("needs_review log product = %v, want nil", review.ProductID)
	}
	if !review.NeedsReview {
		t.Error("needs_review flag not set")
	}
	if len(m.products) != 1 {
		t.Errorf("needs_review created a product: %d rows", len(m.products))
	}
}

func TestCommitRejectsUnknownAction(t *testing.T) {
	m := newMemStores()
	m.seedKnownProduct()
	svc := newTestService(m)
	res := previewForTest(t, svc, m, 1)

	_, err := svc.CommitPreview(context.Background(), CommitRequest{
		UserID: 1, PreviewID: res.PreviewID, Confirm: true,
		Resolutions: []Resolution{{Line: 2, Action: "delete_everything"}},
	})
	if !errors.Is(err, ErrInvalidResolution) {
		t.Fatalf("err = %v, want ErrInvalidResolution", err)
	}
}

func TestCommitRejectsResolutionOnAutoSaveLine(t *testing.T) {
	m := newMemStores()
	m.seedKnownProduct()
	svc := newTestService(m)
	res := previewForTest(t, svc, m, 1)

	_, err := svc.CommitPreview(context.Background(), CommitRequest{
		UserID: 1, PreviewID: res.PreviewID, Confirm: true,
		Resolutions: []Resolution{{Line: 1, Action: ActionAssign, ProductID: 1}},
	})
	if !errors.Is(err, ErrInvalidResolution) {
		t.Fatalf("err = %v, want ErrInvalidResolution", err)
	}
}

func TestCommitDoubleIsConflict(t *testing.T) {
	m := newMemStores()
	m.seedKnownProduct()
	svc := newTestService(m)
	res := previewForTest(t, svc, m, 1)

	if _, err := svc.CommitPreview(context.Background(), CommitRequest{
		UserID: 1, PreviewID: res.PreviewID, Confirm: true,
	}); err != nil {
		t.Fatalf("first commit: %v", err)
	}

	_, err := svc.CommitPreview(context.Background(), CommitRequest{
		UserID: 1, PreviewID: res.PreviewID, Confirm: true,
	})
	if !errors.Is(err, ErrAlreadyCommitted) {
		t.Fatalf("err = %v, want ErrAlreadyCommitted", err)
	}
}

func TestCommitExpiredIsUnprocessable(t *testing.T) {
	m := newMemStores()
	m.seedKnownProduct()
	svc := newTestService(m)
	res := previewForTest(t, svc, m, 1)

	p, _ := m.Previews().GetByID(context.Background(), res.PreviewID)
	p.TTL = time.Now().Add(-time.Minute)

	_, err := svc.CommitPreview(context.Background(), CommitRequest{
		UserID: 1, PreviewID: res.PreviewID, Confirm: true,
	})
	if !errors.Is(err, ErrPreviewExpired) {
		t.Fatalf("err = %v, want ErrPreviewExpired", err)
	}
}

func TestCommitForeignPreviewNotFound(t *testing.T) {
	m := newMemStores()
	m.seedKnownProduct()
	svc := newTestService(m)
	res := previewForTest(t, svc, m, 1)

	_, err := svc.CommitPreview(context.Background(), CommitRequest{
		UserID: 2, PreviewID: res.PreviewID, Confirm: true,
	})
	if !errors.Is(err, ErrPreviewNotFound) {
		t.Fatalf("err = %v, want ErrPreviewNotFound", err)
	}
}

// TestCommitReingestIsNoOp: preview→commit, then a brand-new preview for the
// same access_key commits to the existing nfce row and inserts nothing new.
func TestCommitReingestIsNoOp(t *testing.T) {
	m := newMemStores()
	m.seedKnownProduct()
	svc := newTestService(m)

	first := previewForTest(t, svc, m, 1)
	firstCommit, err := svc.CommitPreview(context.Background(), CommitRequest{
		UserID: 1, PreviewID: first.PreviewID, Confirm: true,
	})
	if err != nil {
		t.Fatalf("first commit: %v", err)
	}
	if firstCommit.Written != 2 {
		t.Fatalf("first commit wrote %d, want 2", firstCommit.Written)
	}
	logsBefore := len(m.priceLogs)

	// A second, independent preview of the same receipt (fresh draft, the old
	// one is already committed and its (user, access_key) slot is free).
	second, err := svc.ReportPreview(context.Background(), PreviewRequest{
		UserID: 1, AccessKey: testAccessKey,
	})
	if err != nil {
		t.Fatalf("second preview: %v", err)
	}

	secondCommit, err := svc.CommitPreview(context.Background(), CommitRequest{
		UserID: 1, PreviewID: second.PreviewID, Confirm: true,
	})
	if err != nil {
		t.Fatalf("second commit: %v", err)
	}
	if secondCommit.Written != 0 {
		t.Errorf("re-ingest wrote %d new rows, want 0", secondCommit.Written)
	}
	if len(m.priceLogs) != logsBefore {
		t.Errorf("price_logs grew from %d to %d", logsBefore, len(m.priceLogs))
	}
	if secondCommit.NfceID != firstCommit.NfceID {
		t.Errorf("nfce id = %d, want %d (row must be reused)", secondCommit.NfceID, firstCommit.NfceID)
	}
	if len(m.nfces) != 1 {
		t.Errorf("nfces = %d, want 1", len(m.nfces))
	}
}

// TestCommitCrossUserDedup answers the shared-receipt question: two users
// draft the same access key, then both confirm. Both commits succeed; only
// the first writes rows, the second is a silent no-op over the same nfce row
// (nfces.access_key + price_logs(nfce_id,line) keep the DB at one copy).
func TestCommitCrossUserDedup(t *testing.T) {
	m := newMemStores()
	m.seedKnownProduct()
	svc := newTestService(m)

	user1 := previewForTest(t, svc, m, 1)
	firstCommit, err := svc.CommitPreview(context.Background(), CommitRequest{
		UserID: 1, PreviewID: user1.PreviewID, Confirm: true,
	})
	if err != nil {
		t.Fatalf("user 1 commit: %v", err)
	}
	if firstCommit.Written == 0 {
		t.Fatalf("user 1 wrote nothing: %+v", firstCommit)
	}

	user2 := previewForTest(t, svc, m, 2)
	secondCommit, err := svc.CommitPreview(context.Background(), CommitRequest{
		UserID: 2, PreviewID: user2.PreviewID, Confirm: true,
	})
	if err != nil {
		t.Fatalf("user 2 commit: %v", err)
	}
	if secondCommit.Written != 0 {
		t.Errorf("user 2 wrote %d rows, want 0 (silent dedup)", secondCommit.Written)
	}
	if secondCommit.NfceID != firstCommit.NfceID {
		t.Errorf("user 2 nfce id = %d, want %d (receipt must be shared)", secondCommit.NfceID, firstCommit.NfceID)
	}
	if len(m.nfces) != 1 {
		t.Errorf("nfces = %d, want a single row for the shared receipt", len(m.nfces))
	}
	if len(m.priceLogs) != firstCommit.Written {
		t.Errorf("price_logs = %d, want %d (no duplicate lines)", len(m.priceLogs), firstCommit.Written)
	}
}

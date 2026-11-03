package service

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"meupreco/internal/domain"
	"meupreco/internal/nfce"
)

// countingResolver records portal fetches so a test can prove draft calls the
// resolver exactly once and submit never calls it.
type countingResolver struct {
	receipt *domain.Nfce
	calls   int
	err     error
}

func (r *countingResolver) Resolve(_ context.Context, _ nfce.AccessKey, _ string) (*domain.Nfce, error) {
	r.calls++
	if r.err != nil {
		return nil, r.err
	}
	return r.receipt, nil
}

// TestCommitIsDatabaseOnlyAfterDraft is the Phase 4 guarantee: once /nfce/new
// has stored the snapshot, /nfce/submit commits without touching the portal.
func TestCommitIsDatabaseOnlyAfterDraft(t *testing.T) {
	m := newMemStores()
	m.seedKnownProduct()
	resolver := &countingResolver{receipt: testReceipt()}
	svc := New(m, resolver, nil, nil)
	ctx := context.Background()

	draft, err := svc.ReportPreview(ctx, PreviewRequest{UserID: 1, AccessKey: testAccessKey})
	if err != nil {
		t.Fatalf("draft: %v", err)
	}
	if resolver.calls != 1 {
		t.Fatalf("resolver calls after draft = %d, want 1", resolver.calls)
	}

	dry, err := svc.CommitPreview(ctx, CommitRequest{
		UserID: 1, PreviewID: draft.PreviewID, Confirm: false,
	})
	if err != nil {
		t.Fatalf("dry-run commit: %v", err)
	}
	if dry.Confirmed || dry.Written != 0 {
		t.Errorf("dry run wrote: %+v", dry)
	}
	if resolver.calls != 1 {
		t.Fatalf("resolver calls after dry run = %d, want 1 (draft only)", resolver.calls)
	}

	// Any portal call during submit is a regression, not a retry: fail loudly.
	resolver.err = errors.New("portal must not be reached during submit")

	out, err := svc.CommitPreview(ctx, CommitRequest{
		UserID: 1, PreviewID: draft.PreviewID, Confirm: true,
	})
	if err != nil {
		t.Fatalf("commit from snapshot: %v", err)
	}
	if resolver.calls != 1 {
		t.Fatalf("resolver calls after submit = %d, want 1 (draft only)", resolver.calls)
	}
	if out.Written != 2 || out.AutoSaved != 1 || out.Review != 1 {
		t.Errorf("commit result = %+v, want 2 written / 1 auto_saved / 1 review", out)
	}
	if _, ok := m.nfces[testAccessKey]; !ok {
		t.Error("receipt was not committed from the snapshot")
	}
	if _, ok := m.supermarkets[testReceipt().Supermarket.CNPJ]; !ok {
		t.Error("supermarket was not upserted from the snapshot")
	}
}

// TestCommitRejectsInvalidSnapshot proves every missing/corrupt/unreadable
// snapshot fails explicitly and never falls back to the resolver.
func TestCommitRejectsInvalidSnapshot(t *testing.T) {
	cases := []struct {
		name    string
		mutate  func(*domain.Preview)
		wantErr error
	}{
		{
			name:    "missing",
			mutate:  func(p *domain.Preview) { p.ReceiptSnapshot = nil },
			wantErr: ErrReceiptSnapshotMissing,
		},
		{
			name:    "corrupt json",
			mutate:  func(p *domain.Preview) { p.ReceiptSnapshot = json.RawMessage("{") },
			wantErr: ErrReceiptSnapshotCorrupt,
		},
		{
			name: "unsupported version",
			mutate: func(p *domain.Preview) {
				p.ReceiptSnapshot = json.RawMessage(`{"schema_version":99,"receipt":{}}`)
			},
			wantErr: ErrReceiptSnapshotUnsupported,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := newMemStores()
			m.seedKnownProduct()
			resolver := &countingResolver{receipt: testReceipt()}
			svc := New(m, resolver, nil, nil)
			ctx := context.Background()

			draft, err := svc.ReportPreview(ctx, PreviewRequest{UserID: 1, AccessKey: testAccessKey})
			if err != nil {
				t.Fatalf("draft: %v", err)
			}
			stored, err := m.Previews().GetByID(ctx, draft.PreviewID)
			if err != nil {
				t.Fatalf("load draft: %v", err)
			}
			tc.mutate(stored)
			resolver.err = errors.New("portal must not be reached during submit")

			if _, err := svc.CommitPreview(ctx, CommitRequest{
				UserID: 1, PreviewID: draft.PreviewID, Confirm: true,
			}); !errors.Is(err, tc.wantErr) {
				t.Fatalf("commit error = %v, want %v", err, tc.wantErr)
			}
			if resolver.calls != 1 {
				t.Fatalf("resolver calls = %d, want 1 (draft only, no fallback)", resolver.calls)
			}
		})
	}
}

// TestCommitRejectsSnapshotAccessKeyMismatch pins the cross-receipt guard: a
// snapshot whose access key differs from the preview's is rejected explicitly.
func TestCommitRejectsSnapshotAccessKeyMismatch(t *testing.T) {
	m := newMemStores()
	m.seedKnownProduct()
	resolver := &countingResolver{receipt: testReceipt()}
	svc := New(m, resolver, nil, nil)
	ctx := context.Background()

	draft, err := svc.ReportPreview(ctx, PreviewRequest{UserID: 1, AccessKey: testAccessKey})
	if err != nil {
		t.Fatalf("draft: %v", err)
	}

	other := testReceipt()
	other.AccessKey = "35241152323688000144650020000002571115675902"
	raw, err := encodeReceiptSnapshot(other)
	if err != nil {
		t.Fatalf("encode mismatch snapshot: %v", err)
	}
	stored, err := m.Previews().GetByID(ctx, draft.PreviewID)
	if err != nil {
		t.Fatalf("load draft: %v", err)
	}
	stored.ReceiptSnapshot = raw

	resolver.err = errors.New("portal must not be reached during submit")
	_, err = svc.CommitPreview(ctx, CommitRequest{
		UserID: 1, PreviewID: draft.PreviewID, Confirm: true,
	})
	if err == nil || !strings.Contains(err.Error(), "snapshot mismatch") {
		t.Fatalf("commit error = %v, want an explicit snapshot mismatch", err)
	}
	if resolver.calls != 1 {
		t.Fatalf("resolver calls = %d, want 1 (draft only)", resolver.calls)
	}
}

// TestDraftPersistsCompleteReceiptSnapshot verifies the persisted snapshot
// carries the supermarket and every receipt line submit needs to write logs.
func TestDraftPersistsCompleteReceiptSnapshot(t *testing.T) {
	m := newMemStores()
	m.seedKnownProduct()
	svc := New(m, stubResolver{receipt: testReceipt()}, nil, nil)
	ctx := context.Background()

	draft, err := svc.ReportPreview(ctx, PreviewRequest{UserID: 1, AccessKey: testAccessKey})
	if err != nil {
		t.Fatalf("draft: %v", err)
	}
	stored, err := m.Previews().GetByID(ctx, draft.PreviewID)
	if err != nil {
		t.Fatalf("load draft: %v", err)
	}

	receipt, err := decodeReceiptSnapshot(stored.ReceiptSnapshot)
	if err != nil {
		t.Fatalf("decode persisted snapshot: %v", err)
	}
	wantReceipt := testReceipt()
	if receipt.AccessKey != wantReceipt.AccessKey {
		t.Errorf("snapshot access key = %q, want %q", receipt.AccessKey, wantReceipt.AccessKey)
	}
	if receipt.Supermarket.CNPJ != wantReceipt.Supermarket.CNPJ ||
		receipt.Supermarket.LegalName != wantReceipt.Supermarket.LegalName {
		t.Errorf("snapshot supermarket = %+v, want %+v", receipt.Supermarket, wantReceipt.Supermarket)
	}
	if len(receipt.Items) != len(wantReceipt.Items) {
		t.Fatalf("snapshot items = %d, want %d", len(receipt.Items), len(wantReceipt.Items))
	}
	for i := range wantReceipt.Items {
		want := wantReceipt.Items[i]
		got := receipt.Items[i]
		if got.Line != want.Line || got.Code != want.Code || got.Description != want.Description ||
			got.Unit != want.Unit {
			t.Errorf("snapshot item %d = %+v, want %+v", i, got, want)
		}
		if !got.Qty.Equal(want.Qty) || !got.UnitPrice.Equal(want.UnitPrice) ||
			!got.TotalPrice.Equal(want.TotalPrice) {
			t.Errorf("snapshot item %d amounts = qty %s / unit %s / total %s, want qty %s / unit %s / total %s",
				i, got.Qty, got.UnitPrice, got.TotalPrice, want.Qty, want.UnitPrice, want.TotalPrice)
		}
	}
}

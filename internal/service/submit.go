package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"meupreco/internal/domain"
	"meupreco/internal/nfce"
)

// ReportPreview implements the draft - half of the two-phase commit — it used
// to be the "preview" step and is now POST /nfce/new: fetch the receipt
// run every line through the identity classifier and persist only the *plan* as a pending previews row.
// Nothing is written to the shared tables before the commit confirms.
//
// Idempotency is the receipt's own access key: at most one live "pending"
// draft may exist per (user, access_key) — replaying POST /nfce/new for the
// same receipt returns the pending plan unchanged (replay-safe, no SEFAZ
// refetch); an expired one is purged and re-planned. A committed receipt is
// invisible here: it simply gets a fresh draft and the subsequent submit is a
// silent no-op.
func (s *Service) ReportPreview(ctx context.Context, req PreviewRequest) (*PreviewResult, error) {
	key, _, err := parseReceiptRequest(req.AccessKey, req.QRPayload)
	if err != nil {
		return nil, err
	}

	s.gc(ctx)

	if existing, err := s.Stores.Previews().GetPendingByUserAccessKey(ctx, req.UserID, key.String()); err == nil {
		if !s.now().After(existing.TTL) {
			return s.decodePreview(existing)
		}
		// Expired: drop it so the fresh plan can reuse the (user, access_key)
		// slot without colliding with the partial unique index.
		if _, delErr := s.Stores.Previews().PurgeExpired(ctx); delErr != nil {
			s.Log.Debug("service: purge expired preview", "err", delErr)
		}
	} else if !errors.Is(err, domain.ErrNotFound) {
		return nil, err
	}

	receipt, err := s.resolveReceipt(ctx, req.AccessKey, req.QRPayload)
	if err != nil {
		return nil, err
	}

	return s.persistDraft(ctx, req.UserID, key, receipt)
}

// persistDraft writes a pending previews row for (userID, key) from an
// already-resolved receipt — the tail of the draft path, shared by
// POST /nfce/new (ReportPreview) and the captcha solve handler. A concurrent
// draft that won the same (user, access_key) pending slot replays instead.
func (s *Service) persistDraft(ctx context.Context, userID int64, key nfce.AccessKey, receipt *domain.Nfce) (*PreviewResult, error) {
	plan, totals := buildPlan(ctx, s.Stores, receipt)

	itemsJSON, err := json.Marshal(plan)
	if err != nil {
		return nil, fmt.Errorf("service: encode plan: %w", err)
	}
	totalsJSON, err := json.Marshal(totals)
	if err != nil {
		return nil, fmt.Errorf("service: encode totals: %w", err)
	}

	p := &domain.Preview{
		UserID:     userID,
		AccessKey:  key.String(),
		PlanItems:  itemsJSON,
		PlanTotals: totalsJSON,
		Status:     "pending",
		TTL:        s.now().Add(s.PreviewTTL),
	}

	if err := s.Stores.Previews().Create(ctx, p); err != nil {
		// A concurrent draft won the same (user, access_key) between our read
		// and insert; return its plan as our answer.
		if errors.Is(err, domain.ErrConflict) {
			if existing, err2 := s.Stores.Previews().GetPendingByUserAccessKey(ctx, userID, key.String()); err2 == nil {
				return s.decodePreview(existing)
			}
		}
		return nil, err
	}

	return &PreviewResult{
		PreviewID: p.ID,
		Status:    "pending",
		AccessKey: p.AccessKey,
		ExpiresAt: p.TTL,
		Plan:      plan,
		Totals:    totals,
	}, nil
}

// decodePreview rebuilds a PreviewResult from an existing pending previews row.
func (s *Service) decodePreview(p *domain.Preview) (*PreviewResult, error) {
	plan, totals, err := decodePlan(p)
	if err != nil {
		return nil, err
	}
	return &PreviewResult{
		PreviewID: p.ID,
		Status:    "pending",
		AccessKey: p.AccessKey,
		ExpiresAt: p.TTL,
		Plan:      plan,
		Totals:    totals,
	}, nil
}

// decodePlan unmarshals the JSON payloads a previews row carries.
func decodePlan(p *domain.Preview) ([]PlanItem, PlanTotals, error) {
	var plan []PlanItem
	if err := json.Unmarshal(p.PlanItems, &plan); err != nil {
		return nil, PlanTotals{}, fmt.Errorf("service: corrupt preview plan_items: %w", err)
	}
	var totals PlanTotals
	if len(p.PlanTotals) > 0 {
		if err := json.Unmarshal(p.PlanTotals, &totals); err != nil {
			return nil, PlanTotals{}, fmt.Errorf("service: corrupt preview plan_totals: %w", err)
		}
	}
	return plan, totals, nil
}

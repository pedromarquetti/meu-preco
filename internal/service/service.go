// Package service orchestrates the receipt pipeline:
//
// fetch → identity → transient preview → single-transaction commit.
//
// The HTTP layer stays thin; this package owns the plan/commit logic
// and the only places that call Stores.Tx.
package service

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"time"

	"meupreco/internal/domain"
	"meupreco/internal/nfce"
)

// Sentinel errors the HTTP layer maps onto status codes.
var (
	// ErrPreviewNotFound reports a preview_id that does not exist or belongs
	// to another user (the two are indistinguishable on purpose).
	ErrPreviewNotFound = errors.New("preview not found")
	// ErrAlreadyCommitted reports a commit of a preview that was committed
	// before; the API maps it to 409.
	ErrAlreadyCommitted = errors.New("preview already committed")
	// ErrPreviewExpired reports a commit of a pending preview past its ttl;
	// the API maps it to 422.
	ErrPreviewExpired = errors.New("preview expired")
	// ErrInvalidResolution reports a bad resolutions[] entry (unknown action,
	// assign without product_id, product_id not in the plan, or a resolution
	// for a line that does not need one). The API maps it to 400.
	ErrInvalidResolution = errors.New("invalid resolution")
	// ErrInvalidRequest reports a malformed commit input (missing preview_id).
	// The API maps it to 400 invalid_request rather than a generic 500.
	ErrInvalidRequest = errors.New("invalid request")
	// ErrProductNotFound reports one product id that does not exist. 404.
	ErrProductNotFound = errors.New("product not found")
	// ErrSupermarketNotFound reports one supermarket that does not exist. 404.
	ErrSupermarketNotFound = errors.New("supermarket not found")
	// ErrReceiptNotFound reports an access key that was never ingested. 404.
	ErrReceiptNotFound = errors.New("receipt not found")
	// ErrUserNotFound reports a target user id that does not exist. 404.
	ErrUserNotFound = errors.New("user not found")
	// ErrForbidden reports an operation the caller may not perform (deleting
	// someone else's receipt, self-demotion). 403.
	ErrForbidden = errors.New("forbidden")
	// ErrInUse reports a guarded delete refused because rows still reference
	// the target (a product still carried by price_logs, a supermarket still
	// owning receipts) when no purge was requested. 409.
	ErrInUse = errors.New("resource in use")
	// ErrInvalidFilter reports an unparseable read query (bad page/limit or an
	// unknown unit/gtin token). 400.
	ErrInvalidFilter = errors.New("invalid filter")
	// ErrInvalidAssignment reports a malformed admin assignment/merge (no
	// price_log_id, missing product_id, unknown action, self-merge). 400.
	ErrInvalidAssignment = errors.New("invalid assignment")
	// ErrInvalidRole reports a role value outside {"user","admin"}. 400.
	ErrInvalidRole = errors.New("invalid role")
)

// Resolver is the fetch+parse slice every submit path needs: given a chave
// (and optionally the raw QR payload), it returns the normalized, privacy-
// stripped receipt. Underlying implementations can return nfce.CaptchaError,
// nfce.ErrKeyNotFound, nfce.ErrInvalidKey, nfce.ErrInvalidQRPayload,
// nfce.ErrUnsupportedUF or nfce.ErrUpstream/EerrBadResponse, which the HTTP
// layer maps onto its typed responses.
type Resolver interface {
	Resolve(ctx context.Context, key nfce.AccessKey, payload string) (*domain.Nfce, error)
}

// SolvedCache is the opt-in half of the resolver surface the captcha flow
// needs: after a challenge is solved, the raw DANFE is cached by access key so
// the follow-up fetch (the /nfce/submit re-fetch) serves it without another
// portal challenge. Implemented by *nfce.Resolver; unused by stub resolvers.
type SolvedCache interface {
	CacheSolved(key nfce.AccessKey, raw []byte)
}

// CaptchaSupport wires the captcha-solving loop into the pipeline: where the
// challenges live, who replays the answer against the portal, and the attempt
// budget. Nil on a Service disables the captcha endpoints.
type CaptchaSupport struct {
	Store       *nfce.ChallengeStore
	Solver      nfce.CaptchaSolver
	MaxAttempts int
}

// PlanItem is one receipt line's identity verdict, the unit of both the
// preview payload and (after a resolution) the commit. It mirrors what
// product.Decision produced for the line, in a JSON-stable shape that rides
// on the previews row.
type PlanItem struct {
	Line         int     `json:"line"`
	Description  string  `json:"description"`
	Status       string  `json:"status"`
	Message      string  `json:"message,omitempty"`
	ProductKey   string  `json:"product_key,omitempty"`
	ProductName  string  `json:"product_name,omitempty"`
	Brand        string  `json:"brand,omitempty"`
	GTIN         string  `json:"gtin,omitempty"`
	Unit         string  `json:"unit,omitempty"`
	PricePerUnit string  `json:"price_per_unit,omitempty"`
	Similarity   float64 `json:"similarity,omitempty"`
}

// PlanTotals is the receipt-level summary the client renders next to the
// itemized plan.
type PlanTotals struct {
	Items          int    `json:"items"`
	AutoSave       int    `json:"auto_save"`
	UIDUncertain   int    `json:"uid_uncertain"`
	NeedsReview    int    `json:"needs_review"`
	ParseErrors    int    `json:"parse_errors"`
	EstimatedTotal string `json:"estimated_total"`
}

// PreviewRequest is the input to ReportPreview. Idempotency is the receipt's
// access key (or the key embedded in qr_payload): no client idempotency field.
type PreviewRequest struct {
	UserID    int64
	AccessKey string
	QRPayload string
}

// PreviewResult is the itemized plan bound to a pending previews row.
type PreviewResult struct {
	PreviewID int64      `json:"preview_id"`
	Status    string     `json:"status"`
	AccessKey string     `json:"access_key"`
	ExpiresAt time.Time  `json:"expires_at"`
	Plan      []PlanItem `json:"items"`
	Totals    PlanTotals `json:"totals"`
}

// Resolution tells the commit what to do with one uid_uncertain line.
// Action is one of "needs_review" (the default when omitted; persists a
// private row with no product), "skip" (explicit opt-out; writes nothing) or
// "assign" (existing product_id only).
type Resolution struct {
	Line      int    `json:"line"`
	Action    string `json:"action"`
	ProductID int64  `json:"product_id,omitempty"`
}

// CommitRequest is the input to CommitPreview.
type CommitRequest struct {
	UserID      int64
	PreviewID   int64
	Confirm     bool
	Resolutions []Resolution
}

// CommitResult reports what the confirming transaction actually wrote.
type CommitResult struct {
	PreviewID int64  `json:"preview_id"`
	Confirmed bool   `json:"confirmed"`
	Written   int    `json:"written"`      // price_log rows inserted
	AutoSaved int    `json:"auto_saved"`   // barcoded/commodity rows
	Assigned  int    `json:"assigned"`     // uid_uncertain rows resolved to a product
	Review    int    `json:"needs_review"` // uid_uncertain rows kept private
	Skipped   int    `json:"skipped"`      // lines written nothing (skip/unparseable/dup)
	NfceID    int64  `json:"nfce_id,omitempty"`
	AccessKey string `json:"access_key"`
}

// Service wires the pipeline and owns the transaction boundary.
type Service struct {
	Stores     domain.Stores
	Resolver   Resolver
	Log        *slog.Logger
	PreviewTTL time.Duration
	Captcha    *CaptchaSupport
	// Enricher augments gtin_new drafts with catalog metadata (opt-in, see
	// cfg EnableOFFEnrich). Nil disables enrichment.
	Enricher OFFEnricher
	now      func() time.Time
}

// New builds the service. log and captcha may be nil (discarded/disabled).
func New(stores domain.Stores, res Resolver, log *slog.Logger, captcha *CaptchaSupport) *Service {
	if log == nil {
		log = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	return &Service{
		Stores:     stores,
		Resolver:   res,
		Log:        log,
		PreviewTTL: 15 * time.Minute,
		Captcha:    captcha,
		now:        time.Now,
	}
}

// gc sweeps stale previews before a write so the table cannot grow without
// bound. Best-effort: a failed sweep never fails the request it guards.
func (s *Service) gc(ctx context.Context) {
	if err := purgeExpired(ctx, s.Log, s.Stores.Previews()); err != nil {
		s.Log.Debug("service: preview GC failed", "err", err)
	}
}

// parseReceiptRequest normalizes the access key (or the QR payload that
// embeds it) without touching the portal. It is the pure-validation half of
// resolveReceipt and lets ReportPreview run its replay lookup cheaply.
func parseReceiptRequest(accessKey, qrPayload string) (nfce.AccessKey, string, error) {
	if qrPayload != "" {
		payload, key, err := nfce.ParseQRPayload(qrPayload)
		if err != nil {
			return nfce.AccessKey{}, "", err
		}
		if accessKey != "" && key.String() != accessKey {
			return nfce.AccessKey{}, "", errors.Join(nfce.ErrInvalidQRPayload,
				errors.New("access_key and the key embedded in qr_payload are different"))
		}
		return key, payload, nil
	}

	key, err := nfce.ParseAccessKey(accessKey)
	if err != nil {
		return nfce.AccessKey{}, "", err
	}
	return key, "", nil
}

// resolveReceipt fetches and normalizes the receipt through the portal
// resolver — the same path POST /api/v1/nfce/new exposes, minus the HTTP layer.
func (s *Service) resolveReceipt(ctx context.Context, accessKey, qrPayload string) (*domain.Nfce, error) {
	key, payload, err := parseReceiptRequest(accessKey, qrPayload)
	if err != nil {
		return nil, err
	}
	return s.Resolver.Resolve(ctx, key, payload)
}

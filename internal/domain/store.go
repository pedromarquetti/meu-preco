// Package domain defines the entities and the repository interfaces the
// services depend on. Implementing repositories live under internal/store; the
// interfaces live here so service code never imports a specific store.
package domain

import (
	"context"
	"errors"
	"time"
)

// Sentinel errors repositories must translate to so services can distinguish
// "not found" from "conflict" without importing a driver.
var (
	ErrNotFound = errors.New("not found")
	ErrConflict = errors.New("conflict")
)

// UserStore persists accounts.
type UserStore interface {
	Create(ctx context.Context, u *User) error
	GetByID(ctx context.Context, id int64) (*User, error)
	GetByEmail(ctx context.Context, email string) (*User, error)
	// GetByLogin resolves an account by its login identifier: either the
	// (case-insensitive) email or the exact name. An email match wins when a
	// name happens to equal another account's email.
	GetByLogin(ctx context.Context, login string) (*User, error)
	// CountAdmin returns the number of admins (used by the boot-time admin seed).
	CountAdmin(ctx context.Context) (int64, error)
	// List returns the users ordered by created_at, most recent first, plus the
	// total count for pagination.
	List(ctx context.Context, limit, offset int) ([]User, int64, error)
	// UpdateRole changes a user's role ("user"|"admin").
	UpdateRole(ctx context.Context, id int64, role string) error
}

// RefreshTokenStore persists rotating refresh-token families. Hashes are
// sha256(refresh token) — the raw JWT never touches the database.
type RefreshTokenStore interface {
	Create(ctx context.Context, t *RefreshToken) error
	// Rotate atomically revokes the row matching oldHash (FOR UPDATE) and
	// inserts next in the same family. Returns ErrNotFound when oldHash is
	// unknown (reused/rotated-away token) and ErrConflict when the row was
	// already revoked.
	Rotate(ctx context.Context, oldHash string, next *RefreshToken) error
	// RevokeFamily marks every token of a family as revoked (reuse detected).
	RevokeFamily(ctx context.Context, familyID string) error
}

// SupermarketStore persists receipt emitters, upserting by CNPJ.
type SupermarketStore interface {
	Upsert(ctx context.Context, s *Supermarket) error
	GetByID(ctx context.Context, id int64) (*Supermarket, error)
	GetByCNPJ(ctx context.Context, cnpj string) (*Supermarket, error)
	// List returns the known supermarkets ordered by legal_name.
	List(ctx context.Context, limit, offset int) ([]Supermarket, int64, error)
	// RePointNfces moves every receipt from one supermarket to another (admin
	// merge by CNPJ variants); returns how many receipts were moved.
	RePointNfces(ctx context.Context, fromID, toID int64) (int64, error)
	// CountNfces reports how many receipts a supermarket still owns.
	CountNfces(ctx context.Context, id int64) (int64, error)
	// Delete removes a supermarket with no remaining receipts.
	Delete(ctx context.Context, id int64) error
}

// ProductStore persists the product master data.
type ProductStore interface {
	Create(ctx context.Context, p *Product) (bool, error)
	GetByID(ctx context.Context, id int64) (*Product, error)
	GetByKey(ctx context.Context, key string) (*Product, error)
	GetByGTIN(ctx context.Context, gtin string) (*Product, error)
	Search(ctx context.Context, query string, limit, offset int) ([]Product, error)
	// UpsertMany batch-inserts products keyed on product_key, updating the
	// row when it already exists; returns how many rows were created new.
	// Used by the OFF bulk seeder, which must be safe to re-run.
	UpsertMany(ctx context.Context, products []*Product) (int, error)
	// NearestMatch returns the product most similar to stem (pg_trgm over
	// name/brand/canonical_stem) together with its similarity score, or
	// ErrNotFound when no row clears the threshold.
	NearestMatch(ctx context.Context, stem string, threshold float64) (*Product, float64, error)
	// SearchFiltered ranks products by pg_trgm similarity across name, brand
	// and canonical_stem, constrained by the non-empty filter fields, and
	// always excludes needs_review rows. It returns the page plus the total
	// number of matches (for stable pagination).
	SearchFiltered(ctx context.Context, f ProductFilter, limit, offset int) ([]Product, int64, error)
	// Update applies the non-nil fields of patch to a product (admin curation).
	Update(ctx context.Context, id int64, patch ProductPatch) error
	// Delete removes a product row (the caller must have re-pointed or
	// cleared its price_logs first so the FK stays whole).
	Delete(ctx context.Context, id int64) error
}

// NfceStore persists normalized receipts, one per access key.
type NfceStore interface {
	Create(ctx context.Context, n *Nfce) error
	GetByID(ctx context.Context, id int64) (*Nfce, error)
	GetByAccessKey(ctx context.Context, accessKey string) (*Nfce, error)
	// Delete removes a receipt row (the caller must have cleared its
	// price_logs and preview drafts first for referential integrity).
	Delete(ctx context.Context, id int64) error
}

// PriceLogStore persists individual receipt lines.
type PriceLogStore interface {
	Create(ctx context.Context, l *PriceLog) error
	GetByID(ctx context.Context, id int64) (*PriceLog, error)
	// GetByNfceLine returns the log for a receipt line, or ErrNotFound.
	GetByNfceLine(ctx context.Context, nfceID int64, line int) (*PriceLog, error)
	// HistoryByProduct returns the price curve for a product, each point
	// annotated with its receipt and supermarket, optionally filtered by the
	// receipt's issued_at window.
	HistoryByProduct(ctx context.Context, productID int64, from, to *time.Time) ([]PricePoint, error)
	// LatestBySupermarket returns the products sold at a supermarket with
	// their most recent logged price, excluding needs_review rows, plus the
	// distinct-product total for pagination.
	LatestBySupermarket(ctx context.Context, supermarketID int64, limit, offset int) ([]SupermarketProduct, int64, error)
	// NeedsReview returns the admin review queue: price_logs rows flagged
	// needs_review, annotated with store code, CNPJ base and store name, plus
	// the queue size for pagination.
	NeedsReview(ctx context.Context, limit, offset int) ([]ReviewItem, int64, error)
	// RePointProduct re-attaches every log to another product (admin merge);
	// returns how many logs moved.
	RePointProduct(ctx context.Context, fromID, toID int64) (int64, error)
	// SetProduct re-points one log to a product and clears its needs_review
	// flag (admin batch-assign). heldBy frees the "(user_id, access_key)
	// pending" slot after the review, so the queue stays clean.
	SetProduct(ctx context.Context, priceLogID, productID int64, resolvedBy int64, note string) error
	// DeleteByNfceID removes every log of a receipt; returns the removed count.
	DeleteByNfceID(ctx context.Context, nfceID int64) (int64, error)
	// DeleteByProduct removes every log referencing a product; returns the
	// removed count. Used by the guarded product delete (?purge=true).
	DeleteByProduct(ctx context.Context, productID int64) (int64, error)
	// CountByProduct reports how many logs still reference a product (the
	// delete guard).
	CountByProduct(ctx context.Context, productID int64) (int64, error)
}

// PreviewStore persists the transient two-phase-commit records.
type PreviewStore interface {
	Create(ctx context.Context, p *Preview) error
	GetByID(ctx context.Context, id int64) (*Preview, error)
	// GetPendingByUserAccessKey returns the caller's live pending draft for a
	// receipt (one per (user, access_key), partial unique index), or
	// ErrNotFound. This is the receipt-idempotency lookup: re-new-ing the same
	// receipt replays its draft instead of stacking a duplicate.
	GetPendingByUserAccessKey(ctx context.Context, userID int64, accessKey string) (*Preview, error)
	// MarkCommitted atomically flips a pending preview to committed; returns
	// ErrConflict if it was already committed, ErrNotFound if missing.
	MarkCommitted(ctx context.Context, id int64) error
	// PurgeExpired deletes preview rows past their ttl (written/preview GC).
	// It returns how many rows were removed.
	PurgeExpired(ctx context.Context) (int64, error)
	// DeleteByAccessKey removes every preview draft of a receipt across all
	// users (reported when the receipt is deleted). Returns the removed count.
	DeleteByAccessKey(ctx context.Context, accessKey string) (int64, error)
}

// CommoditySynonymStore persists the produce taxonomy used for GTIN-less
// identity resolution.
type CommoditySynonymStore interface {
	Create(ctx context.Context, s *CommoditySynonym) error
	// Canonicalize returns the canonical synonym row for an exact stem match.
	Canonicalize(ctx context.Context, synonym string) (*CommoditySynonym, error)
}

// CommodityBrandStore reads the curated brand vocabulary that separates
// GTIN-less commodity identities (Tirolez vs Vigor cheese).
type CommodityBrandStore interface {
	// List returns every curated brand ordered by brand key.
	List(ctx context.Context) ([]CommodityBrand, error)
}

// Stores bundles the per-entity repositories that one database handle serves.
type Stores interface {
	Users() UserStore
	Supermarkets() SupermarketStore
	Products() ProductStore
	Nfces() NfceStore
	PriceLogs() PriceLogStore
	Previews() PreviewStore
	CommoditySynonyms() CommoditySynonymStore
	Brands() CommodityBrandStore
	RefreshTokens() RefreshTokenStore
	// Ping verifies the database is reachable (used by /readyz).
	Ping(ctx context.Context) error
	// Close releases the underlying pool.
	Close()
	// Tx runs fn inside a single database transaction: the store handed to fn
	// shares the caller's transaction, and a non-nil error rolls everything
	// back (the transaction commits only when fn returns nil).
	Tx(ctx context.Context, fn func(tx Stores) error) error
}

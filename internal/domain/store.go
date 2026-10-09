// Package domain defines the entities and the repository interfaces the
// services depend on. Implementing repositories live under internal/store; the
// interfaces live here so service code never imports a specific store.
package domain

import (
	"context"
	"errors"
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
	// CountAdmin returns the number of admins (used by the boot-time admin seed).
	CountAdmin(ctx context.Context) (int64, error)
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
}

// ProductStore persists the product master data.
type ProductStore interface {
	Create(ctx context.Context, p *Product) error
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
}

// NfceStore persists normalized receipts, one per access key.
type NfceStore interface {
	Create(ctx context.Context, n *Nfce) error
	GetByID(ctx context.Context, id int64) (*Nfce, error)
	GetByAccessKey(ctx context.Context, accessKey string) (*Nfce, error)
}

// PriceLogStore persists individual receipt lines.
type PriceLogStore interface {
	Create(ctx context.Context, l *PriceLog) error
	GetByID(ctx context.Context, id int64) (*PriceLog, error)
	// GetByNfceLine returns the log for a receipt line, or ErrNotFound.
	GetByNfceLine(ctx context.Context, nfceID int64, line int) (*PriceLog, error)
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
}

// CommoditySynonymStore persists the produce taxonomy used for GTIN-less
// identity resolution.
type CommoditySynonymStore interface {
	Create(ctx context.Context, s *CommoditySynonym) error
	// Canonicalize returns the canonical synonym row for an exact stem match.
	Canonicalize(ctx context.Context, synonym string) (*CommoditySynonym, error)
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

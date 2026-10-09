package domain

import (
	"encoding/json"
	"time"

	"github.com/shopspring/decimal"
)

// User is an account able to read shared price data and submit receipts.
type User struct {
	ID           int64
	Email        string
	PasswordHash string
	Role         string // "user" | "admin"
	CreatedAt    time.Time
}

// RefreshToken tracks one live refresh-token family row so rotation and
// reuse detection can be enforced server-side. TokenHash is sha256(token).
type RefreshToken struct {
	ID        int64
	UserID    int64
	FamilyID  string
	TokenHash string
	Revoked   bool
	ExpiresAt time.Time
	CreatedAt time.Time
}

// Product is the canonical master-data row for one sellable item, keyed by
// product_key (a 14-digit GTIN or a deterministic commodity hash).
type Product struct {
	ID            int64
	ProductKey    string // gtin(14) or sha256(canonical_stem|unit)
	GTIN          *string
	Name          string
	Brand         *string
	Unit          string
	CanonicalStem *string
	NeedsReview   bool
	CreatedBy     *int64
	CreatedAt     time.Time
}

// PriceLog is a single receipt line saved to the shared price history.
type PriceLog struct {
	ID                  int64
	NfceID              int64
	ProductID           *int64
	Line                int
	DescriptionSnapshot string
	Qty                 decimal.Decimal
	Unit                string
	NormalizedUnit      *string
	PricePerUnit        *decimal.Decimal
	UnitPrice           decimal.Decimal
	TotalPrice          decimal.Decimal
	IdentityStatus      string
	NeedsReview         bool
	ResolvedBy          *int64
	ResolutionNote      *string
	CreatedAt           time.Time
}

// Preview is the transient, two-phase-commit record binding an ingestion plan
// to a single confirm step. plan_items/plan_totals ride as JSON. Idempotency
// is the receipt's access key: at most one live "pending" row may exist for a
// (user_id, access_key) pair (enforced by a partial unique index).
type Preview struct {
	ID         int64
	UserID     int64
	AccessKey  string
	PlanItems  json.RawMessage // JSON array
	PlanTotals json.RawMessage // JSON object
	Status     string          // "pending" | "committed" | "expired"
	TTL        time.Time
	CreatedAt  time.Time
}

// CommoditySynonym maps a normalized synonym stem to a canonical commodity
// identity for GTIN-less goods (fresh produce, bulk, cheeses).
type CommoditySynonym struct {
	ID            int64
	Synonym       string
	CanonicalName string
	Category      string
	Unit          string
	Source        string
	CreatedAt     time.Time
}

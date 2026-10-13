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
	StoreCode           string // cProd as printed on the receipt
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

// ProductFilter is the read-side product search predicate. Empty fields are
// ignored; the store always excludes needs_review rows. GTIN is matched as a
// normalized digit prefix ("7831" finds "07891000412855").
type ProductFilter struct {
	Query string // trigram match over name/brand/canonical_stem
	GTIN  string // digits-only barcode prefix
	Unit  string // exact canonical unit token
	Brand string // substring on brand
}

// ProductPatch is the admin curation edit: nil fields keep the current value,
// a non-nil field overwrites it (brand set to &("") clears the brand).
type ProductPatch struct {
	Name  *string
	Brand *string
	Unit  *string
}

// PricePoint is one line of a product's price curve: the price_logs row joined
// with its receipt and emitter so the read is self-annotating per supermarket.
type PricePoint struct {
	PriceLogID     int64
	NfceID         int64
	AccessKey      string
	SupermarketID  int64
	StoreCNPJ      string
	StoreName      string
	Line           int
	Qty            decimal.Decimal
	Unit           string
	NormalizedUnit *string
	PricePerUnit   *decimal.Decimal
	UnitPrice      decimal.Decimal
	TotalPrice     decimal.Decimal
	IssuedAt       *time.Time
	LoggedAt       time.Time
}

// SupermarketProduct is a product sold at one supermarket, carrying its most
// recent logged price for that store (used by GET /supermarkets/{id}/products).
type SupermarketProduct struct {
	Product        Product
	PriceLogID     int64
	Qty            decimal.Decimal
	NeedsReview    bool
	NormalizedUnit *string
	PricePerUnit   *decimal.Decimal
	UnitPrice      decimal.Decimal
	TotalPrice     decimal.Decimal
	LastIssuedAt   *time.Time
	LastLoggedAt   time.Time
}

// ReviewItem is one row of the admin needs-review queue: a price_logs row
// flagged uid_uncertain→needs_review, annotated with the store code (cProd),
// the emitter's CNPJ base and store name so an admin can act on it.
type ReviewItem struct {
	PriceLogID    int64
	NfceID        int64
	AccessKey     string
	Line          int
	Description   string // raw xProd
	StoreCode     string // cProd as printed on the receipt
	CNPJBase      string // first 8 CNPJ digits
	StoreName     string
	Unit          string
	Qty           decimal.Decimal
	PricePerUnit  *decimal.Decimal
	UnitPrice     decimal.Decimal
	TotalPrice    decimal.Decimal
	SuggestedStem string // best-guess canonical stem for batch-assign
	CreatedAt     time.Time
}

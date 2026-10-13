package service

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/shopspring/decimal"

	"meupreco/internal/domain"
	"meupreco/internal/product"
)

// Read-only Phase 7 endpoints: shared product/supermarket reads, all behind
// RequireAuth. Money fields are strings so the JSON payload never transports a
// float; the canonical unit pair (KG/UN/GR/ML/LT) carries the price_per_unit
// basis explicitly per point.

// Page is the stable pagination envelope shared by every read endpoint.
type Page struct {
	Page       int   `json:"page"`
	Limit      int   `json:"limit"`
	Total      int64 `json:"total"`
	TotalPages int64 `json:"total_pages"`
}

// ProductView is the public product projection (never a needs_review row).
type ProductView struct {
	ID            int64     `json:"id"`
	ProductKey    string    `json:"product_key"`
	GTIN          *string   `json:"gtin,omitempty"`
	Name          string    `json:"name"`
	Brand         *string   `json:"brand,omitempty"`
	Unit          string    `json:"unit"`
	CanonicalStem *string   `json:"canonical_stem,omitempty"`
	CreatedAt     time.Time `json:"created_at"`
}

// ProductPage is the search result page.
type ProductPage struct {
	Items []ProductView `json:"items"`
	Page  Page          `json:"page"`
}

// SupermarketRef is the minimal emitter projection carried by price points.
type SupermarketRef struct {
	ID        int64  `json:"id"`
	CNPJ      string `json:"cnpj"`
	LegalName string `json:"legal_name"`
}

// SupermarketView is the supermarkets-list projection.
type SupermarketView struct {
	ID        int64     `json:"id"`
	CNPJ      string    `json:"cnpj"`
	LegalName string    `json:"legal_name"`
	Address   string    `json:"address,omitempty"`
	CreatedAt time.Time `json:"created_at"`
}

// SupermarketPage is the paginated store list.
type SupermarketPage struct {
	Items []SupermarketView `json:"items"`
	Page  Page              `json:"page"`
}

// SupermarketProductView is one product sold at a store, with its most recent
// logged price.
type SupermarketProductView struct {
	Product        ProductView `json:"product"`
	PriceLogID     int64       `json:"price_log_id"`
	Qty            string      `json:"qty"`
	NormalizedUnit *string     `json:"normalized_unit,omitempty"`
	PricePerUnit   *string     `json:"price_per_unit,omitempty"`
	UnitPrice      string      `json:"unit_price"`
	TotalPrice     string      `json:"total_price"`
	LastIssuedAt   *time.Time  `json:"last_issued_at,omitempty"`
	LastLoggedAt   time.Time   `json:"last_logged_at"`
}

// SupermarketProductsPage is the per-store catalog page.
type SupermarketProductsPage struct {
	Supermarket SupermarketView          `json:"supermarket"`
	Items       []SupermarketProductView `json:"items"`
	Page        Page                     `json:"page"`
}

// PricePointView is one point of a product's price curve. comparable_* carry
// the aggregation basis: DZ prices fold into the per-piece UN series (÷12) so
// mixed-unit curves are comparable; price_per_unit stays the raw stored value.
type PricePointView struct {
	AccessKey              string         `json:"access_key"`
	Supermarket            SupermarketRef `json:"supermarket"`
	Line                   int            `json:"line"`
	Qty                    string         `json:"qty"`
	Unit                   string         `json:"unit"`
	NormalizedUnit         *string        `json:"normalized_unit,omitempty"`
	PricePerUnit           *string        `json:"price_per_unit,omitempty"`
	ComparableUnit         string         `json:"comparable_unit"`
	ComparablePricePerUnit *string        `json:"comparable_price_per_unit,omitempty"`
	UnitPrice              string         `json:"unit_price"`
	TotalPrice             string         `json:"total_price"`
	IssuedAt               *time.Time     `json:"issued_at,omitempty"`
	LoggedAt               time.Time      `json:"logged_at"`
}

// UnitStats is the aggregation summary for one comparable unit across the
// curve: min/max/avg/last of the folded per-unit price.
type UnitStats struct {
	Unit  string  `json:"unit"`
	Count int     `json:"count"`
	Min   *string `json:"min,omitempty"`
	Avg   *string `json:"avg,omitempty"`
	Max   *string `json:"max,omitempty"`
	Last  *string `json:"last,omitempty"`

	sum    decimal.Decimal
	minDec *decimal.Decimal
	maxDec *decimal.Decimal
}

// ProductHistory is the full price curve for a product: ordered points plus
// the per-comparable-unit aggregate (the mixed-unit KG/UN/DZ consolidation).
type ProductHistory struct {
	Product   ProductView      `json:"product"`
	From      *time.Time       `json:"from,omitempty"`
	To        *time.Time       `json:"to,omitempty"`
	Points    []PricePointView `json:"points"`
	Aggregate []UnitStats      `json:"aggregate"`
}

// maxPageSize / defaultPageSize bound every read; anything else is a 400.
const (
	defaultPageSize = 20
	maxPageSize     = 100
)

func validatePage(page, limit int) error {
	if page < 1 {
		return ErrInvalidFilter
	}
	if limit < 1 || limit > maxPageSize {
		return ErrInvalidFilter
	}
	return nil
}

func newPage(page, limit int, total int64) Page {
	totalPages := int64(0)
	if limit > 0 && total > 0 {
		totalPages = (total + int64(limit) - 1) / int64(limit)
	}
	return Page{Page: page, Limit: limit, Total: total, TotalPages: totalPages}
}

func toProductView(p domain.Product) ProductView {
	return ProductView{
		ID:            p.ID,
		ProductKey:    p.ProductKey,
		GTIN:          p.GTIN,
		Name:          p.Name,
		Brand:         p.Brand,
		Unit:          p.Unit,
		CanonicalStem: p.CanonicalStem,
		CreatedAt:     p.CreatedAt,
	}
}

func toSupermarketView(s domain.Supermarket) SupermarketView {
	return SupermarketView{
		ID:        s.ID,
		CNPJ:      s.CNPJ,
		LegalName: s.LegalName,
		Address:   s.Address,
		CreatedAt: s.CreatedAt,
	}
}

// SearchProducts implements GET /api/v1/products: pg_trgm search constrained
// by the non-empty filter fields, paginated, needs_review always excluded.
func (s *Service) SearchProducts(ctx context.Context, f domain.ProductFilter, page, limit int) (*ProductPage, error) {
	if err := validatePage(page, limit); err != nil {
		return nil, err
	}
	f.Query = strings.TrimSpace(f.Query)
	f.Brand = strings.TrimSpace(f.Brand)

	if f.GTIN = strings.TrimSpace(f.GTIN); f.GTIN != "" {
		if len(f.GTIN) > 14 || !allDigits(f.GTIN) {
			return nil, ErrInvalidFilter
		}
	}
	if f.Unit = strings.TrimSpace(f.Unit); f.Unit != "" {
		u, ok := product.NormalizeUnit(f.Unit)
		if !ok {
			return nil, ErrInvalidFilter
		}
		f.Unit = u
	}

	items, total, err := s.Stores.Products().SearchFiltered(ctx, f, limit, (page-1)*limit)
	if err != nil {
		return nil, err
	}
	out := make([]ProductView, 0, len(items))
	for _, p := range items {
		out = append(out, toProductView(p))
	}
	return &ProductPage{Items: out, Page: newPage(page, limit, total)}, nil
}

// GetProduct implements GET /api/v1/products/{id}.
func (s *Service) GetProduct(ctx context.Context, id int64) (*ProductView, error) {
	if id <= 0 {
		return nil, ErrProductNotFound
	}
	p, err := s.Stores.Products().GetByID(ctx, id)
	if err != nil {
		if errors.Is(err, domain.ErrNotFound) {
			return nil, ErrProductNotFound
		}
		return nil, err
	}
	v := toProductView(*p)
	return &v, nil
}

// ProductHistory implements GET /api/v1/products/{id}/history: the time series
// of price_per_unit annotated per supermarket, with DZ folded into the UN
// series (÷12) and an aggregated summary per comparable unit. from/to window
// the receipt emission date when present.
func (s *Service) ProductHistory(ctx context.Context, id int64, from, to *time.Time) (*ProductHistory, error) {
	if err := validateWindow(from, to); err != nil {
		return nil, err
	}
	p, err := s.Stores.Products().GetByID(ctx, id)
	if err != nil {
		if errors.Is(err, domain.ErrNotFound) {
			return nil, ErrProductNotFound
		}
		return nil, err
	}

	points, err := s.Stores.PriceLogs().HistoryByProduct(ctx, id, from, to)
	if err != nil {
		return nil, err
	}

	out := &ProductHistory{
		Product: toProductView(*p),
		From:    from,
		To:      to,
	}

	index := map[string]int{}
	for _, pt := range points {
		rawUnit := pt.Unit
		if pt.NormalizedUnit != nil {
			rawUnit = *pt.NormalizedUnit
		}
		comparableUnit := product.ComparableUnit(rawUnit)

		view := PricePointView{
			AccessKey:      pt.AccessKey,
			Supermarket:    SupermarketRef{ID: pt.SupermarketID, CNPJ: pt.StoreCNPJ, LegalName: pt.StoreName},
			Line:           pt.Line,
			Qty:            pt.Qty.String(),
			Unit:           pt.Unit,
			NormalizedUnit: pt.NormalizedUnit,
			ComparableUnit: comparableUnit,
			UnitPrice:      pt.UnitPrice.String(),
			TotalPrice:     pt.TotalPrice.String(),
			IssuedAt:       pt.IssuedAt,
			LoggedAt:       pt.LoggedAt,
		}
		if pt.PricePerUnit != nil {
			raw := pt.PricePerUnit.String()
			view.PricePerUnit = &raw
			comp := product.ComparePricePerUnit(rawUnit, *pt.PricePerUnit)
			compStr := comp.String()
			view.ComparablePricePerUnit = &compStr

			i, ok := index[comparableUnit]
			if !ok {
				index[comparableUnit] = len(out.Aggregate)
				out.Aggregate = append(out.Aggregate, UnitStats{Unit: comparableUnit})
				i = index[comparableUnit]
			}
			st := &out.Aggregate[i]
			st.Count++
			st.sum = st.sum.Add(comp)
			st.Last = &compStr
			if st.Min == nil || comp.LessThan(*st.minDec) {
				m := comp.String()
				st.Min = &m
				st.minDec = &comp
			}
			if st.Max == nil || comp.GreaterThan(*st.maxDec) {
				m := comp.String()
				st.Max = &m
				st.maxDec = &comp
			}
		}
		out.Points = append(out.Points, view)
	}

	for i := range out.Aggregate {
		if out.Aggregate[i].Count > 0 {
			avg := out.Aggregate[i].sum.Div(decimal.NewFromInt(int64(out.Aggregate[i].Count))).String()
			out.Aggregate[i].Avg = &avg
		}
	}

	return out, nil
}

// ListSupermarkets implements GET /api/v1/supermarkets.
func (s *Service) ListSupermarkets(ctx context.Context, page, limit int) (*SupermarketPage, error) {
	if err := validatePage(page, limit); err != nil {
		return nil, err
	}
	items, total, err := s.Stores.Supermarkets().List(ctx, limit, (page-1)*limit)
	if err != nil {
		return nil, err
	}
	out := make([]SupermarketView, 0, len(items))
	for _, sm := range items {
		out = append(out, toSupermarketView(sm))
	}
	return &SupermarketPage{Items: out, Page: newPage(page, limit, total)}, nil
}

// SupermarketProducts implements GET /api/v1/supermarkets/{id}/products.
func (s *Service) SupermarketProducts(ctx context.Context, supermarketID int64, page, limit int) (*SupermarketProductsPage, error) {
	if err := validatePage(page, limit); err != nil {
		return nil, err
	}
	sm, err := s.Stores.Supermarkets().GetByID(ctx, supermarketID)
	if err != nil {
		if errors.Is(err, domain.ErrNotFound) {
			return nil, ErrSupermarketNotFound
		}
		return nil, err
	}

	items, total, err := s.Stores.PriceLogs().LatestBySupermarket(ctx, supermarketID, limit, (page-1)*limit)
	if err != nil {
		return nil, err
	}

	out := make([]SupermarketProductView, 0, len(items))
	for _, sp := range items {
		v := SupermarketProductView{
			Product:        toProductView(sp.Product),
			PriceLogID:     sp.PriceLogID,
			Qty:            sp.Qty.String(),
			NormalizedUnit: sp.NormalizedUnit,
			UnitPrice:      sp.UnitPrice.String(),
			TotalPrice:     sp.TotalPrice.String(),
			LastIssuedAt:   sp.LastIssuedAt,
			LastLoggedAt:   sp.LastLoggedAt,
		}
		if sp.PricePerUnit != nil {
			ppu := sp.PricePerUnit.String()
			v.PricePerUnit = &ppu
		}
		out = append(out, v)
	}

	return &SupermarketProductsPage{
		Supermarket: toSupermarketView(*sm),
		Items:       out,
		Page:        newPage(page, limit, total),
	}, nil
}

func validateWindow(from, to *time.Time) error {
	if from != nil && to != nil && from.After(*to) {
		return ErrInvalidFilter
	}
	return nil
}

func allDigits(s string) bool {
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return s != ""
}

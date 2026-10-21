package product

import (
	"context"
	"errors"
	"testing"

	"github.com/shopspring/decimal"

	"meupreco/internal/domain"
)

// fakeCatalog implements Catalog against in-memory maps.
type fakeCatalog struct {
	byGTIN     map[string]*domain.Product
	byKey      map[string]*domain.Product
	synonyms   map[string]*domain.CommoditySynonym
	nearest    *nearestResult
	gtinErr    error
	keyErr     error
	synErr     error
	nearestErr error
}

type nearestResult struct {
	p   *domain.Product
	sim float64
	err error
}

func (f *fakeCatalog) GetByGTIN(_ context.Context, gtin string) (*domain.Product, error) {
	if f.gtinErr != nil {
		return nil, f.gtinErr
	}
	if p, ok := f.byGTIN[gtin]; ok {
		return p, nil
	}
	return nil, domain.ErrNotFound
}

func (f *fakeCatalog) GetByKey(_ context.Context, key string) (*domain.Product, error) {
	if f.keyErr != nil {
		return nil, f.keyErr
	}
	if p, ok := f.byKey[key]; ok {
		return p, nil
	}
	return nil, domain.ErrNotFound
}

func (f *fakeCatalog) NearestMatch(_ context.Context, _ string, _ float64) (*domain.Product, float64, error) {
	if f.nearestErr != nil {
		return nil, 0, f.nearestErr
	}
	if f.nearest == nil {
		return nil, 0, domain.ErrNotFound
	}
	return f.nearest.p, f.nearest.sim, f.nearest.err
}

func (f *fakeCatalog) Canonicalize(_ context.Context, synonym string) (*domain.CommoditySynonym, error) {
	if f.synErr != nil {
		return nil, f.synErr
	}
	if s, ok := f.synonyms[synonym]; ok {
		return s, nil
	}
	return nil, domain.ErrNotFound
}

func req(t *testing.T, qty, total, unitPrice string) Line {
	t.Helper()
	return Line{
		Description: "LIMAO TAHITI KG",
		Code:        "7896008038717",
		Qty:         decimal.RequireFromString(qty),
		Unit:        "KG",
		TotalPrice:  decimal.RequireFromString(total),
	}
}

func TestClassifyParseError(t *testing.T) {
	cat := &fakeCatalog{}
	ctx := context.Background()

	empty := Line{}
	if d := Classify(ctx, cat, empty, nil); d.Status != StatusParseError {
		t.Errorf("empty desc status = %q, want parse_error (%s)", d.Status, d.Message)
	}

	noQty := Line{Description: "ARROZ BRANCO", Qty: decimal.Zero, Unit: "UN"}
	if d := Classify(ctx, cat, noQty, nil); d.Status != StatusParseError {
		t.Errorf("zero qty status = %q, want parse_error", d.Status)
	}

	neg := Line{Description: "X", Qty: decimal.NewFromInt(-1), Unit: "UN"}
	if d := Classify(ctx, cat, neg, nil); d.Status != StatusParseError {
		t.Errorf("neg qty status = %q, want parse_error", d.Status)
	}
}

func TestClassifyGtinKnown(t *testing.T) {
	cat := &fakeCatalog{byGTIN: map[string]*domain.Product{
		"07896008038717": {ProductKey: "07896008038717", Name: "Pão de Forma", Unit: "UN"},
	}}
	d := Classify(context.Background(), cat, req(t, "1", "10", "10"), nil)
	if d.Status != StatusGtinKnown {
		t.Fatalf("status = %q, want gtin_known", d.Status)
	}
	if d.ProductKey != "07896008038717" {
		t.Errorf("product key = %q", d.ProductKey)
	}
	if !d.HasPricePerUnit || !d.PricePerUnit.Equal(decimal.RequireFromString("10")) {
		t.Errorf("price_per_unit = %v (has=%v)", d.PricePerUnit, d.HasPricePerUnit)
	}
}

func TestClassifyGtinNew(t *testing.T) {
	cat := &fakeCatalog{} // no rows
	d := Classify(context.Background(), cat, req(t, "1", "10", "10"), nil)
	if d.Status != StatusGtinNew {
		t.Fatalf("status = %q, want gtin_new", d.Status)
	}
	if d.ProductKey != "07896008038717" || d.GTIN != "07896008038717" {
		t.Errorf("key/gtin = %q/%q", d.ProductKey, d.GTIN)
	}
	if d.Name != "LIMAO TAHITI KG" {
		t.Errorf("name = %q (should snapshot the description)", d.Name)
	}
}

func TestClassifyCommodityFingerprintExisting(t *testing.T) {
	key := CommodityKey("limao tahiti", "UN", "")
	cat := &fakeCatalog{
		synonyms: map[string]*domain.CommoditySynonym{
			"limao tahiti": {CanonicalName: "limao tahiti", Category: "hortifruti", Unit: "UN"},
		},
		byKey: map[string]*domain.Product{
			key: {ProductKey: key, Name: "Limão Tahiti", Unit: "UN"},
		},
	}
	line := Line{Description: "LIMAO TAHITI KG", Qty: decimal.NewFromInt(2), Unit: "KG", TotalPrice: decimal.NewFromInt(4)}
	d := Classify(context.Background(), cat, line, nil)
	if d.Status != StatusCommodityFingerprint {
		t.Fatalf("status = %q, want commodity_fingerprint", d.Status)
	}
	if d.Name != "Limão Tahiti" {
		t.Errorf("name = %q (canonical from catalog)", d.Name)
	}
}

func TestClassifyCommodityFingerprintCreatable(t *testing.T) {
	cat := &fakeCatalog{
		synonyms: map[string]*domain.CommoditySynonym{
			"limao tahiti": {CanonicalName: "limao tahiti", Unit: "UN"},
		},
		// no product row yet → still fingerprint, key derivable
	}
	line := Line{Description: "LIMAO TAHITI KG", Qty: decimal.NewFromInt(2), Unit: "KG", TotalPrice: decimal.NewFromInt(4)}
	d := Classify(context.Background(), cat, line, nil)
	if d.Status != StatusCommodityFingerprint {
		t.Fatalf("status = %q, want commodity_fingerprint", d.Status)
	}
	if d.ProductKey != CommodityKey("limao tahiti", "UN", "") {
		t.Errorf("product key = %q", d.ProductKey)
	}
}

func TestClassifyCommodityNearestHighSimilarity(t *testing.T) {
	cat := &fakeCatalog{nearest: &nearestResult{
		p:   &domain.Product{ProductKey: "key-frango", Name: "Frango Inteiro", Unit: "KG"},
		sim: 0.61,
	}}
	line := Line{Description: "FRANGO INTEIRO 1KG", Qty: decimal.NewFromInt(1), Unit: "KG", TotalPrice: decimal.NewFromInt(12)}
	d := Classify(context.Background(), cat, line, nil)
	if d.Status != StatusCommodityFingerprint {
		t.Fatalf("status = %q, want commodity_fingerprint", d.Status)
	}
	if d.Name != "Frango Inteiro" {
		t.Errorf("name = %q", d.Name)
	}
}

func TestClassifyUIDUncertain(t *testing.T) {
	t.Run("no canonical, low similarity", func(t *testing.T) {
		cat := &fakeCatalog{nearest: &nearestResult{
			p:   &domain.Product{ProductKey: "x", Name: "Arroz", Unit: "KG"},
			sim: 0.31,
		}}
		line := Line{Description: "SALSA DE PIMENTA FERMENTADA ARTESANAL", Qty: decimal.NewFromInt(1), Unit: "UN", TotalPrice: decimal.NewFromInt(9)}
		d := Classify(context.Background(), cat, line, nil)
		if d.Status != StatusUIDUncertain {
			t.Fatalf("status = %q, want uid_uncertain", d.Status)
		}
	})
	t.Run("no canonical, no nearest", func(t *testing.T) {
		cat := &fakeCatalog{} // nearest returns ErrNotFound
		line := Line{Description: "SALSA DE PIMENTA FERMENTADA ARTESANAL", Qty: decimal.NewFromInt(1), Unit: "UN", TotalPrice: decimal.NewFromInt(9)}
		d := Classify(context.Background(), cat, line, nil)
		if d.Status != StatusUIDUncertain {
			t.Fatalf("status = %q, want uid_uncertain", d.Status)
		}
	})
	t.Run("catalog lookup failure degrades", func(t *testing.T) {
		cat := &fakeCatalog{gtinErr: errors.New("db down")}
		d := Classify(context.Background(), cat, req(t, "1", "10", "10"), nil)
		if d.Status != StatusUIDUncertain {
			t.Fatalf("status = %q, want uid_uncertain (graceful)", d.Status)
		}
	})
	t.Run("garbage description", func(t *testing.T) {
		cat := &fakeCatalog{}
		line := Line{Description: "!!!### 12345", Qty: decimal.NewFromInt(1), Unit: "UN", TotalPrice: decimal.NewFromInt(1)}
		d := Classify(context.Background(), cat, line, nil)
		if d.Status != StatusUIDUncertain {
			t.Fatalf("status = %q, want uid_uncertain (garbage)", d.Status)
		}
	})
}

func TestClassifyBadCodeFallsToCommodity(t *testing.T) {
	// An invalid check digit is not a GTIN: it must not be treated as
	// gtin_new and must fall through to the commodity tier.
	cat := &fakeCatalog{}
	line := Line{Description: "LIMAO TAHITI KG", Code: "7896008038713", Qty: decimal.NewFromInt(1), Unit: "KG", TotalPrice: decimal.NewFromInt(3)}
	d := Classify(context.Background(), cat, line, nil)
	if d.Status != StatusUIDUncertain {
		t.Fatalf("invalid check digit status = %q, want uid_uncertain", d.Status)
	}
}

func TestClassifyForeignPrefixFallback(t *testing.T) {
	// Non-789/790 code without a BR valid GTIN.
	cat := &fakeCatalog{}
	line := Line{Description: "ARROZ BRANCO", Code: "5901234123457", Qty: decimal.NewFromInt(1), Unit: "PK", TotalPrice: decimal.NewFromInt(5)}
	d := Classify(context.Background(), cat, line, nil)
	if d.Status != StatusUIDUncertain {
		t.Fatalf("foreign prefix status = %q, want uid_uncertain", d.Status)
	}
}

// The seed catalogue mirrors migrations/000002 (limao tahiti UN), proving the
// DoD case end-to-end with the real lookup surfaces.
func TestClassifySeedCatalogLimao(t *testing.T) {
	key := CommodityKey("limao tahiti", "UN", "")
	cat := &fakeCatalog{
		synonyms: map[string]*domain.CommoditySynonym{
			"limao tahiti": {CanonicalName: "limao tahiti", Category: "hortifruti", Unit: "UN", Source: "CONAB PROHORT / CEAGESP"},
		},
		byKey: map[string]*domain.Product{
			key: {ProductKey: key, Name: "Limão Tahiti", Unit: "UN", CanonicalStem: stringPtr("limao tahiti")},
		},
	}
	line := Line{Description: "LIMAO TAHITI KG", Qty: decimal.RequireFromString("0.815"), Unit: "KG", TotalPrice: decimal.RequireFromString("4.88")}
	d := Classify(context.Background(), cat, line, nil)
	if d.Status != StatusCommodityFingerprint {
		t.Fatalf("LIMAO TAHITI KG status = %q, want commodity_fingerprint", d.Status)
	}
	if d.ProductKey != key {
		t.Errorf("product key = %q, want %q", d.ProductKey, key)
	}
	if !d.HasPricePerUnit || !d.PricePerUnit.Round(4).Equal(decimal.RequireFromString("5.9877")) {
		t.Errorf("price_per_unit = %v (want 5.9877/KG)", d.PricePerUnit.StringFixed(4))
	}
}

func stringPtr(s string) *string { return &s }

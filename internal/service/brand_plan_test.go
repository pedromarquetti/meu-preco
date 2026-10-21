package service

import (
	"context"
	"testing"

	"github.com/shopspring/decimal"

	"meupreco/internal/domain"
	"meupreco/internal/product"
)

// TestPreviewAndCommitBrandAwareCommodity replays the live COVABRA Limeira
// receipts: "QJ BRIE TIROLEZ KG" must resolve to the queijo-brie commodity
// scoped to Tirolez, and the commit must persist that brand and canonical stem
// onto the created product row.
func TestPreviewAndCommitBrandAwareCommodity(t *testing.T) {
	m := newMemStores()
	m.seedBrand("Tirolez")
	m.seedBrand("Vigor")
	for syn, canon := range map[string]string{"queijo brie": "queijo brie", "qj brie": "queijo brie", "qjo brie": "queijo brie"} {
		if err := m.CommoditySynonyms().Create(context.Background(), &domain.CommoditySynonym{
			Synonym: syn, CanonicalName: canon, Category: "laticinios", Unit: "KG",
		}); err != nil {
			t.Fatalf("seed synonym %q: %v", syn, err)
		}
	}

	receipt := &domain.Nfce{
		AccessKey:   testAccessKey,
		Supermarket: domain.Supermarket{CNPJ: "12345678000199", LegalName: "COVABRA LTDA"},
		Total:       decimal.RequireFromString("10.00"),
		Items: []domain.NfceItem{
			{
				Line: 1, Code: "", Description: "QJ BRIE TIROLEZ KG",
				Qty: decimal.NewFromInt(1), Unit: "KG",
				UnitPrice: decimal.RequireFromString("10"), TotalPrice: decimal.RequireFromString("10"),
			},
		},
	}
	svc := New(m, stubResolver{receipt: receipt}, nil, nil)

	res, err := svc.ReportPreview(context.Background(), PreviewRequest{UserID: 1, AccessKey: testAccessKey})
	if err != nil {
		t.Fatalf("preview: %v", err)
	}
	if len(res.Plan) != 1 {
		t.Fatalf("plan items = %d, want 1", len(res.Plan))
	}
	item := res.Plan[0]
	wantKey := product.CommodityKey("queijo brie", "KG", "Tirolez")
	if item.Status != product.StatusCommodityFingerprint {
		t.Fatalf("status = %q, want commodity_fingerprint", item.Status)
	}
	if item.Brand != "Tirolez" || item.CanonicalStem != "queijo brie" || item.ProductKey != wantKey {
		t.Errorf("plan item = %+v, want brand=Tirolez stem=queijo brie key=%s", item, wantKey)
	}

	out, err := svc.CommitPreview(context.Background(), CommitRequest{UserID: 1, PreviewID: res.PreviewID, Confirm: true})
	if err != nil {
		t.Fatalf("commit: %v", err)
	}
	if out.AutoSaved != 1 {
		t.Errorf("commit result = %+v, want 1 auto-saved", out)
	}

	p, err := m.Products().GetByKey(context.Background(), wantKey)
	if err != nil {
		t.Fatalf("product not created: %v", err)
	}
	if p.Brand == nil || *p.Brand != "Tirolez" {
		t.Errorf("stored brand = %v, want Tirolez", p.Brand)
	}
	if p.CanonicalStem == nil || *p.CanonicalStem != "queijo brie" {
		t.Errorf("stored canonical stem = %v, want queijo brie", p.CanonicalStem)
	}

	if _, err := m.Products().GetByKey(context.Background(), product.CommodityKey("queijo brie", "KG", "")); err == nil {
		t.Error("brandless brie key must not exist alongside the branded one")
	}
}

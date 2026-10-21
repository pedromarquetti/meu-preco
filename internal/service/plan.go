package service

import (
	"context"
	"fmt"

	"meupreco/internal/domain"
	"meupreco/internal/product"
)

// catalogAdapter adapts domain.Stores to product.Catalog so the classifier
// can run against whatever store backend the service holds.
type catalogAdapter struct {
	stores domain.Stores
}

func (c catalogAdapter) GetByGTIN(ctx context.Context, gtin string) (*domain.Product, error) {
	return c.stores.Products().GetByGTIN(ctx, gtin)
}

func (c catalogAdapter) GetByKey(ctx context.Context, key string) (*domain.Product, error) {
	return c.stores.Products().GetByKey(ctx, key)
}

func (c catalogAdapter) NearestMatch(ctx context.Context, stem string, threshold float64) (*domain.Product, float64, error) {
	return c.stores.Products().NearestMatch(ctx, stem, threshold)
}

func (c catalogAdapter) Canonicalize(ctx context.Context, synonym string) (*domain.CommoditySynonym, error) {
	return c.stores.CommoditySynonyms().Canonicalize(ctx, synonym)
}

// brandDictionary loads the curated brand vocabulary. It is required for
// commodity classification because silently disabling it can merge products
// from different brands under the same key.
func brandDictionary(ctx context.Context, stores domain.Stores) (*product.BrandDictionary, error) {
	brands, err := stores.Brands().List(ctx)
	if err != nil {
		return nil, fmt.Errorf("load commodity brands: %w", err)
	}

	entries := make([]product.BrandEntry, 0, len(brands))

	for _, b := range brands {
		entries = append(entries, product.BrandEntry{Key: b.Brand, Display: b.Display})
	}

	return product.NewBrandDictionaryEntries(entries), nil
}

// buildPlan classifies every receipt line and produces the itemized plan plus
// its summary. The brand dictionary is loaded from the curated commodity_brands
// table so known brands are peeled off GTIN-less descriptions and stored on the
// resolved product.
func buildPlan(ctx context.Context, stores domain.Stores, receipt *domain.Nfce) ([]PlanItem, PlanTotals, error) {
	cat := catalogAdapter{stores: stores}

	brands, err := brandDictionary(ctx, stores)
	if err != nil {
		return nil, PlanTotals{}, err
	}

	plan := make([]PlanItem, 0, len(receipt.Items))
	var totals PlanTotals
	totals.EstimatedTotal = receipt.Total.String()

	for _, item := range receipt.Items {
		dec := product.Classify(ctx, cat, product.Line{
			Description: item.Description,
			Code:        item.Code,
			Qty:         item.Qty,
			Unit:        item.Unit,
			TotalPrice:  item.TotalPrice,
		}, brands)

		pi := PlanItem{
			Line:          item.Line,
			Description:   item.Description,
			Status:        dec.Status,
			ProductKey:    dec.ProductKey,
			ProductName:   dec.Name,
			Brand:         dec.Brand,
			CanonicalStem: dec.CanonicalStem,
			GTIN:          dec.GTIN,
			Unit:          dec.Unit,
			Similarity:    dec.Similarity,
		}
		if dec.HasPricePerUnit {
			// price_logs.price_per_unit is NUMERIC(14,6): round here so the
			// number the client confirms equals the number the DB stores.
			pi.PricePerUnit = dec.PricePerUnit.Round(6).String()
		}

		switch dec.Status {
		case product.StatusGtinKnown:
			pi.Message = fmt.Sprintf("product '%s' known by barcode %s", dec.Name, dec.GTIN)
			totals.AutoSave++
		case product.StatusGtinNew:
			pi.Message = fmt.Sprintf("new barcode %s: '%s' will be created on commit", dec.GTIN, dec.Name)
			totals.AutoSave++
		case product.StatusCommodityFingerprint:
			pi.Message = fmt.Sprintf("matched commodity '%s'", dec.Name)
			totals.AutoSave++
		case product.StatusUIDUncertain:
			if dec.Message == "" {
				dec.Message = "unknown product"
			}
			pi.Message = fmt.Sprintf("product '%s' has unknown id: %s", item.Description, dec.Message)
			totals.UIDUncertain++
		case product.StatusParseError:
			pi.Message = "line could not be classified: " + dec.Message
			totals.ParseErrors++
		}

		plan = append(plan, pi)
		totals.Items++
	}

	return plan, totals, nil
}

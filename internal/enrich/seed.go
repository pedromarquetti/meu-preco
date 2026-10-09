package enrich

import (
	"bufio"
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"meupreco/internal/domain"
	"meupreco/internal/product"
)

// SeedStats reports what a bulk import did so callers can log progress.
type SeedStats struct {
	LinesRead int
	// GTINValid is the count of lines whose code normalized to a valid BR
	// GTIN (789|790, check digit OK).
	GTINValid int
	// RowsUpserted is the number of rows written (includes in-place updates);
	// Created is the subset of those that were brand-new.
	RowsUpserted int
	Created      int
	Skipped      int // no valid GTIN / empty name
}

// SeedStore is the slice of domain.ProductStore the seeder needs; tests can
// stub it without implementing the whole interface.
type SeedStore interface {
	UpsertMany(ctx context.Context, products []*domain.Product) (int, error)
}

// SeedFromOFF imports gzipped JSONL exports of the Open Food Facts products
// dump (openfoodfacts-products.jsonl.gz). Each line is one product object:
//
//	{"code":"7896008038717","product_name":"Arroz","brands":"Tio Joao","quantity":"5 kg"}
//
// Only Brazilian GTINs (789|790, valid check digit) are imported; the key is
// the normalized 14-digit barcode, units are inferred from the quantity string
// and default to UN. The import is idempotent: re-running the same file only
// updates existing rows, never duplicates them.
func SeedFromOFF(ctx context.Context, store SeedStore, r io.Reader) (SeedStats, error) {
	var stats SeedStats
	rows := make([]*domain.Product, 0, batchSize)

	commit := func() error {
		if len(rows) == 0 {
			return nil
		}
		created, err := store.UpsertMany(ctx, rows)
		if err != nil {
			return err
		}
		stats.RowsUpserted += len(rows)
		stats.Created += created
		rows = rows[:0]
		return nil
	}

	zr, err := gzip.NewReader(r)
	if err != nil {
		return stats, fmt.Errorf("seed: open gzip: %w", err)
	}
	defer zr.Close()

	sc := bufio.NewScanner(zr)
	sc.Buffer(make([]byte, 0, 1<<20), 4<<20)

	for sc.Scan() {
		stats.LinesRead++
		line := sc.Bytes()
		if len(line) == 0 {
			continue
		}
		var rec struct {
			Code    string `json:"code"`
			Name    string `json:"product_name"`
			Brands  string `json:"brands"`
			Qty     string `json:"quantity"`
			Generic string `json:"generic_name"`
		}
		if err := json.Unmarshal(line, &rec); err != nil {
			stats.Skipped++
			continue
		}
		gtin, ok := product.NormalizeGTIN(rec.Code)
		if !ok {
			stats.Skipped++
			continue
		}
		name := strings.TrimSpace(rec.Name)
		if name == "" {
			name = strings.TrimSpace(rec.Generic)
		}
		if name == "" {
			stats.Skipped++
			continue
		}
		stats.GTINValid++
		unit := inferUnit(rec.Qty)
		if unit == "" {
			unit = product.UnitUN
		}
		brand := strings.TrimSpace(rec.Brands)
		p := &domain.Product{
			ProductKey: gtin,
			GTIN:       &gtin,
			Name:       name,
			Unit:       unit,
		}
		if brand != "" {
			p.Brand = &brand
		}
		rows = append(rows, p)
		if len(rows) >= batchSize {
			if err := commit(); err != nil {
				return stats, err
			}
		}
	}
	if err := sc.Err(); err != nil {
		return stats, fmt.Errorf("seed: read stream: %w", err)
	}
	if err := commit(); err != nil {
		return stats, err
	}
	return stats, nil
}

const batchSize = 500

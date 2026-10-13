package service

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"meupreco/internal/domain"
	"meupreco/internal/enrich"
	"meupreco/internal/product"
)

// OFFEnricher resolves a canonical GTIN to catalog metadata (name/brand).
// *enrich.OFFClient implements it; a nil Enricher on the Service disables
// enrichment entirely.
type OFFEnricher interface {
	Lookup(ctx context.Context, gtin string) (*enrich.Product, error)
}

// purgeExpired deletes stale preview drafts and logs the outcome.
func purgeExpired(ctx context.Context, log *slog.Logger, previews domain.PreviewStore) error {
	n, err := previews.PurgeExpired(ctx)
	if err != nil {
		return err
	}
	if n > 0 {
		log.Debug("service: preview GC removed stale plans", "count", n)
	}
	return nil
}

// RunPreviewGC sweeps expired preview drafts every interval until ctx is
// cancelled. It performs one sweep before entering the loop so a long-idle
// server starts with a spotless drafts table, then keeps the table bounded on
// top of the per-write sweep (Service.gc). Returns ctx.Err() on shutdown.
func RunPreviewGC(ctx context.Context, log *slog.Logger, previews domain.PreviewStore, interval time.Duration) error {
	if interval <= 0 {
		interval = 5 * time.Minute
	}
	if err := purgeExpired(ctx, log, previews); err != nil {
		log.Warn("service: initial preview GC failed", "err", err)
	}

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			if err := purgeExpired(ctx, log, previews); err != nil {
				log.Warn("service: preview GC failed", "err", err)
			}
		}
	}
}

// enrichPlan augments gtin_new plan lines with cached Open Food Facts metadata
// when the service carries an Enricher. Best-effort by design: a lookup
// failure (offline, unknown barcode, timeout) leaves the line's parsed
// identity untouched rather than failing the draft.
func (s *Service) enrichPlan(ctx context.Context, plan []PlanItem) {
	if s.Enricher == nil {
		return
	}
	for i := range plan {
		pi := &plan[i]
		if pi.Status != product.StatusGtinNew || pi.GTIN == "" {
			continue
		}
		p, err := s.Enricher.Lookup(ctx, pi.GTIN)
		if err != nil || p == nil || p.Name == "" {
			continue
		}
		pi.ProductName = p.Name
		if p.Brand != "" {
			pi.Brand = p.Brand
		}
		pi.Message = fmt.Sprintf("new barcode %s: '%s' will be created on commit (enriched from catalog)", pi.GTIN, p.Name)
	}
}

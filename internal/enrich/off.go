// Package enrich pulls product enrichment data from external catalogs and
// seeds the products table in bulk (Open Food Facts).
//
// Data sourced from Open Food Facts is licensed under the Open Database
// License (ODbL) v1.0 (https://opendatacommons.org/licenses/odbl/). We store
// it read-only and attribute generation data to OFF in the product row when
// the source is written (see canonical_stem product metadata conventions).
package enrich

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"meupreco/internal/product"
)

// ErrProductNotFound is returned by OFFClient when the barcode is unknown to
// the catalog. Every other error from Lookup is a transport/temporary failure;
// callers must treat them as "online enrich failed, fall back to gtin_new".
var ErrProductNotFound = errors.New("product not found on Open Food Facts")

// Product is the enriched metadata extracted from an OFF product record. All
// fields are best-effort; callers tolerate empty values.
type Product struct {
	GTIN  string
	Name  string
	Brand string
	Unit  string // canonical unit when the quantity string parses, else ""
}

const defaultUserAgent = "meupreco/1.0 (price transparency)"

// OFFClient talks to the Open Food Facts BR API. It is HTTP-only, has bounded
// per-request timeouts and caches lookups per GTIN with a TTL.
type OFFClient struct {
	baseURL   string
	client    *http.Client
	userAgent string

	mu      sync.Mutex
	cache   map[string]offCacheEntry
	ttl     time.Duration // hits
	missTTL time.Duration // 404/not-found

	hits   atomic.Uint64 // lookups served from the cache
	misses atomic.Uint64 // lookups that reached the upstream API
}

// CacheStats reports the cache hit/miss counts since the client was built.
// The /metrics endpoint exposes them so enrichment cost is observable.
type CacheStats struct {
	Hits   uint64 `json:"hits"`
	Misses uint64 `json:"misses"`
}

// CacheStats returns the accumulated lookup counters.
func (c *OFFClient) CacheStats() CacheStats {
	return CacheStats{Hits: c.hits.Load(), Misses: c.misses.Load()}
}

type offCacheEntry struct {
	product *Product
	// notFound marks a cached negative result (no product, do not re-hit).
	notFound  bool
	expiresAt time.Time
}

// OFFClientOption tunes an OFFClient (used by tests).
type OFFClientOption func(*OFFClient)

// WithBaseURL overrides the API root (tests point at httptest.Server).
func WithBaseURL(u string) OFFClientOption {
	return func(c *OFFClient) { c.baseURL = strings.TrimRight(u, "/") }
}

// WithHTTPClient overrides the underlying HTTP client.
func WithHTTPClient(h *http.Client) OFFClientOption {
	return func(c *OFFClient) { c.client = h }
}

// NewOFFClient returns a client for br.openfoodfacts.org with sane defaults.
func NewOFFClient(opts ...OFFClientOption) *OFFClient {
	c := &OFFClient{
		baseURL:   "https://br.openfoodfacts.org",
		client:    &http.Client{Timeout: 5 * time.Second},
		userAgent: defaultUserAgent,
		cache:     make(map[string]offCacheEntry),
		ttl:       48 * time.Hour,
		missTTL:   6 * time.Hour,
	}
	for _, o := range opts {
		o(c)
	}
	return c
}

// Lookup resolves a canonical 14-digit GTIN against the OFF product API.
//
//	GET {base}/api/v2/product/{gtin}?fields=product_name,brands,quantity
//
// A positive hit returns the enriched product; a known-empty barcode returns
// ErrProductNotFound; anything else (timeouts, 5xx, connection failure) is
// returned as a transport error so the caller can degrade to gtin_new without
// blocking ingestion. Degradation is the design contract: offline OFF must
// never turn a successful fetch into an error cascade.
func (c *OFFClient) Lookup(ctx context.Context, gtin string) (*Product, error) {
	// Defensive: only the canonical 14-digit form is a valid cache/mirror key.
	// A raw 13-digit barcode is normalized so retries collide on one entry.
	if n, ok := product.NormalizeGTIN(gtin); ok {
		gtin = n
	} else {
		return nil, ErrProductNotFound
	}

	if e, ok := c.get(gtin); ok {
		c.hits.Add(1)
		if e.notFound {
			return nil, ErrProductNotFound
		}
		return e.product, nil
	}
	c.misses.Add(1)

	u, err := url.Parse(c.baseURL + "/api/v2/product/" + url.PathEscape(gtin))
	if err != nil {
		return nil, fmt.Errorf("enrich: bad url: %w", err)
	}
	q := u.Query()
	q.Set("fields", "product_name,brands,quantity")
	u.RawQuery = q.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, fmt.Errorf("enrich: build request: %w", err)
	}
	req.Header.Set("User-Agent", c.userAgent)
	req.Header.Set("Accept", "application/json")

	resp, err := c.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("enrich: off unreachable: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound {
		c.remember(gtin, nil, true)
		return nil, ErrProductNotFound
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("enrich: off http %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, fmt.Errorf("enrich: read body: %w", err)
	}

	var payload struct {
		Status  int `json:"status"`
		Product struct {
			Name     string `json:"product_name"`
			Brand    string `json:"brands"`
			Quantity string `json:"quantity"`
		} `json:"product"`
		NotFound string          `json:"not_found"`
		Warnings json.RawMessage `json:"warnings"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, fmt.Errorf("enrich: decode body: %w", err)
	}
	if payload.Status != 1 || payload.Product.Name == "" {
		c.remember(gtin, nil, true)
		return nil, ErrProductNotFound
	}

	p := &Product{
		GTIN:  gtin,
		Name:  strings.TrimSpace(payload.Product.Name),
		Brand: strings.TrimSpace(payload.Product.Brand),
		Unit:  inferUnit(payload.Product.Quantity),
	}
	c.remember(gtin, p, false)
	return p, nil
}

// get returns the cached entry for a GTIN when fresh.
func (c *OFFClient) get(gtin string) (offCacheEntry, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.cache[gtin]
	if !ok || time.Now().After(e.expiresAt) {
		return offCacheEntry{}, false
	}
	return e, true
}

// remember stores an entry with the appropriate TTL (hits live longer than
// not-found negatives).
func (c *OFFClient) remember(gtin string, p *Product, notFound bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	ttl := c.ttl
	if notFound {
		ttl = c.missTTL
	}
	c.cache[gtin] = offCacheEntry{product: p, notFound: notFound, expiresAt: time.Now().Add(ttl)}
}

// inferUnit parses OFF's free-text quantity ("500 g", "1 kg", "3 x 200 ml",
// "350g", "1kg") into a canonical unit token; "" when nothing maps.
func inferUnit(quantity string) string {
	q := strings.ToLower(strings.TrimSpace(quantity))
	if q == "" {
		return ""
	}
	for _, tok := range strings.FieldsFunc(q, func(r rune) bool {
		return r == ' ' || r == 'x' || r == ',' || r == '.'
	}) {
		if u, ok := product.NormalizeUnit(tok); ok {
			return u
		}
		// Glued quantity+unit ("350g") — try the trailing letters.
		if i := strings.IndexFunc(tok, func(r rune) bool { return r < '0' || r > '9' }); i > 0 {
			if u, ok := product.NormalizeUnit(tok[i:]); ok {
				return u
			}
		}
	}
	return ""
}

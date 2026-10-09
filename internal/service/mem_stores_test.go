package service

import (
	"context"
	"errors"
	"strconv"
	"time"

	"meupreco/internal/domain"
)

// memStores is an in-memory domain.Stores for hermetic service tests. Tx runs
// fn against the same underlying maps, so unique-key semantics (dedupe) and
// idempotency-by-status behave like the pg store does. Unused repositories
// are the embedded nil interface; they panic if touched by accident.
type memStores struct {
	domain.Stores

	products    map[string]*domain.Product // by product_key
	byGtin      map[string]*domain.Product // by normalized gtin
	byProductID map[int64]*domain.Product
	nextProdID  int64

	previews   map[int64]*domain.Preview
	previewKey map[string]int64 // "userID:accessKey" -> id while pending
	nextPrevID int64

	supermarkets map[string]*domain.Supermarket // by cnpj
	nextShopID   int64

	nfces      map[string]*domain.Nfce // by access_key
	nextNfceID int64

	priceLogs []*domain.PriceLog
	logKeys   map[string]bool // "nfceID:line"
	nextLogID int64

	synonyms  map[string]*domain.CommoditySynonym
	nextSynID int64
}

func newMemStores() *memStores {
	return &memStores{
		products:     map[string]*domain.Product{},
		byGtin:       map[string]*domain.Product{},
		byProductID:  map[int64]*domain.Product{},
		previews:     map[int64]*domain.Preview{},
		previewKey:   map[string]int64{},
		supermarkets: map[string]*domain.Supermarket{},
		nfces:        map[string]*domain.Nfce{},
		logKeys:      map[string]bool{},
		synonyms:     map[string]*domain.CommoditySynonym{},
	}
}

func (m *memStores) Tx(_ context.Context, fn func(domain.Stores) error) error {
	snap := m.snapshot()
	if err := fn(m); err != nil {
		m.restore(snap)
		return err
	}
	return nil
}

// memSnapshot is a shallow copy of the store maps/slices sufficient to roll
// back a failed Tx: rows added mid-transaction are dropped, counters reset.
type memSnapshot struct {
	products     map[string]*domain.Product
	byGtin       map[string]*domain.Product
	byProductID  map[int64]*domain.Product
	nextProdID   int64
	previews     map[int64]*domain.Preview
	previewKey   map[string]int64
	nextPrevID   int64
	supermarkets map[string]*domain.Supermarket
	nextShopID   int64
	nfces        map[string]*domain.Nfce
	nextNfceID   int64
	priceLogs    []*domain.PriceLog
	logKeys      map[string]bool
	nextLogID    int64
	synonyms     map[string]*domain.CommoditySynonym
	nextSynID    int64
}

func (m *memStores) snapshot() memSnapshot {
	return memSnapshot{
		products:     copyMap(m.products),
		byGtin:       copyMap(m.byGtin),
		byProductID:  copyMap(m.byProductID),
		nextProdID:   m.nextProdID,
		previews:     copyMap(m.previews),
		previewKey:   copyMap(m.previewKey),
		nextPrevID:   m.nextPrevID,
		supermarkets: copyMap(m.supermarkets),
		nextShopID:   m.nextShopID,
		nfces:        copyMap(m.nfces),
		nextNfceID:   m.nextNfceID,
		priceLogs:    append([]*domain.PriceLog(nil), m.priceLogs...),
		logKeys:      copyMap(m.logKeys),
		nextLogID:    m.nextLogID,
		synonyms:     copyMap(m.synonyms),
		nextSynID:    m.nextSynID,
	}
}

func copyMap[M ~map[K]V, K comparable, V any](src M) M {
	dst := make(M, len(src))
	for k, v := range src {
		dst[k] = v
	}
	return dst
}

func (m *memStores) restore(s memSnapshot) {
	m.products = s.products
	m.byGtin = s.byGtin
	m.byProductID = s.byProductID
	m.nextProdID = s.nextProdID
	m.previews = s.previews
	m.previewKey = s.previewKey
	m.nextPrevID = s.nextPrevID
	m.supermarkets = s.supermarkets
	m.nextShopID = s.nextShopID
	m.nfces = s.nfces
	m.nextNfceID = s.nextNfceID
	m.priceLogs = s.priceLogs
	m.logKeys = s.logKeys
	m.nextLogID = s.nextLogID
	m.synonyms = s.synonyms
	m.nextSynID = s.nextSynID
}

// seedKnownProduct installs a barcoded product so a line classifies gtin_known.
func (m *memStores) seedKnownProduct() *domain.Product {
	p := &domain.Product{
		ProductKey: "07891000412855",
		GTIN:       strPtr("07891000412855"),
		Name:       "ACHOCOLATADO EM PO NESCAU 350G",
		Unit:       "GR",
	}
	if err := m.Products().Create(context.Background(), p); err != nil {
		panic(err)
	}
	return p
}

// memProducts implements domain.ProductStore.
type memProducts struct{ m *memStores }

func (s memProducts) Create(_ context.Context, p *domain.Product) error {
	m := s.m
	if _, ok := m.byProductID[p.ID]; ok {
		return domain.ErrConflict
	}
	if _, ok := m.products[p.ProductKey]; ok {
		return domain.ErrConflict
	}
	if p.GTIN != nil {
		if _, ok := m.byGtin[*p.GTIN]; ok {
			return domain.ErrConflict
		}
	}
	m.nextProdID++
	p.ID = m.nextProdID
	m.products[p.ProductKey] = p
	m.byProductID[p.ID] = p
	if p.GTIN != nil {
		m.byGtin[*p.GTIN] = p
	}
	return nil
}

func (s memProducts) GetByID(_ context.Context, id int64) (*domain.Product, error) {
	if p, ok := s.m.byProductID[id]; ok {
		return p, nil
	}
	return nil, domain.ErrNotFound
}

func (s memProducts) GetByKey(_ context.Context, key string) (*domain.Product, error) {
	if p, ok := s.m.products[key]; ok {
		return p, nil
	}
	return nil, domain.ErrNotFound
}

func (s memProducts) GetByGTIN(_ context.Context, gtin string) (*domain.Product, error) {
	if p, ok := s.m.byGtin[gtin]; ok {
		return p, nil
	}
	return nil, domain.ErrNotFound
}

func (s memProducts) Search(context.Context, string, int, int) ([]domain.Product, error) {
	return nil, nil
}

func (s memProducts) UpsertMany(context.Context, []*domain.Product) (int, error) {
	return 0, errors.New("service: UpsertMany not supported in memStores")
}

func (s memProducts) NearestMatch(context.Context, string, float64) (*domain.Product, float64, error) {
	return nil, 0, domain.ErrNotFound
}

// memPreviews implements domain.PreviewStore. The partial unique index of the
// pg schema is honored for the "pending" state only: at most one live draft
// per (user_id, access_key); committed/expired rows free the slot.
type memPreviews struct{ m *memStores }

func previewKeyFor(userID int64, accessKey string) string {
	return strconv.FormatInt(userID, 10) + ":" + accessKey
}

func (s memPreviews) Create(_ context.Context, p *domain.Preview) error {
	m := s.m
	if p.Status == "pending" {
		if _, ok := m.previewKey[previewKeyFor(p.UserID, p.AccessKey)]; ok {
			return domain.ErrConflict
		}
	}
	m.nextPrevID++
	p.ID = m.nextPrevID
	m.previews[p.ID] = p
	if p.Status == "pending" {
		m.previewKey[previewKeyFor(p.UserID, p.AccessKey)] = p.ID
	}
	return nil
}

func (s memPreviews) GetByID(_ context.Context, id int64) (*domain.Preview, error) {
	if p, ok := s.m.previews[id]; ok {
		return p, nil
	}
	return nil, domain.ErrNotFound
}

func (s memPreviews) GetPendingByUserAccessKey(_ context.Context, userID int64, accessKey string) (*domain.Preview, error) {
	if id, ok := s.m.previewKey[previewKeyFor(userID, accessKey)]; ok {
		return s.m.previews[id], nil
	}
	return nil, domain.ErrNotFound
}

func (s memPreviews) MarkCommitted(_ context.Context, id int64) error {
	p, ok := s.m.previews[id]
	if !ok {
		return domain.ErrNotFound
	}
	if p.Status == "committed" {
		return domain.ErrConflict
	}
	p.Status = "committed"
	delete(s.m.previewKey, previewKeyFor(p.UserID, p.AccessKey))
	return nil
}

func (s memPreviews) PurgeExpired(_ context.Context) (int64, error) {
	m := s.m
	now := time.Now()
	var n int64
	for id, p := range m.previews {
		if p.TTL.Before(now) {
			delete(m.previews, id)
			delete(m.previewKey, previewKeyFor(p.UserID, p.AccessKey))
			n++
		}
	}
	return n, nil
}

// memSupermarkets implements domain.SupermarketStore.
type memSupermarkets struct{ m *memStores }

func (s memSupermarkets) Upsert(_ context.Context, sm *domain.Supermarket) error {
	m := s.m
	if existing, ok := m.supermarkets[sm.CNPJ]; ok {
		sm.ID = existing.ID
		sm.CreatedAt = existing.CreatedAt
		existing.LegalName = sm.LegalName
		existing.Address = sm.Address
		return nil
	}
	m.nextShopID++
	sm.ID = m.nextShopID
	m.supermarkets[sm.CNPJ] = sm
	return nil
}

func (s memSupermarkets) GetByID(_ context.Context, id int64) (*domain.Supermarket, error) {
	for _, sm := range s.m.supermarkets {
		if sm.ID == id {
			return sm, nil
		}
	}
	return nil, domain.ErrNotFound
}

func (s memSupermarkets) GetByCNPJ(_ context.Context, cnpj string) (*domain.Supermarket, error) {
	if sm, ok := s.m.supermarkets[cnpj]; ok {
		return sm, nil
	}
	return nil, domain.ErrNotFound
}

// memNfces implements domain.NfceStore.
type memNfces struct{ m *memStores }

func (s memNfces) Create(_ context.Context, n *domain.Nfce) error {
	m := s.m
	if _, ok := m.nfces[n.AccessKey]; ok {
		return domain.ErrConflict
	}
	m.nextNfceID++
	n.ID = m.nextNfceID
	m.nfces[n.AccessKey] = n
	return nil
}

func (s memNfces) GetByID(_ context.Context, id int64) (*domain.Nfce, error) {
	for _, n := range s.m.nfces {
		if n.ID == id {
			return n, nil
		}
	}
	return nil, domain.ErrNotFound
}

func (s memNfces) GetByAccessKey(_ context.Context, key string) (*domain.Nfce, error) {
	if n, ok := s.m.nfces[key]; ok {
		return n, nil
	}
	return nil, domain.ErrNotFound
}

// memPriceLogs implements domain.PriceLogStore with the UNIQUE(nfce_id, line)
// semantics baked in.
type memPriceLogs struct{ m *memStores }

func (s memPriceLogs) Create(_ context.Context, l *domain.PriceLog) error {
	m := s.m
	key := priceLogKey(l.NfceID, l.Line)
	if m.logKeys[key] {
		return domain.ErrConflict
	}
	m.nextLogID++
	l.ID = m.nextLogID
	m.priceLogs = append(m.priceLogs, l)
	m.logKeys[key] = true
	return nil
}

func (s memPriceLogs) GetByID(_ context.Context, id int64) (*domain.PriceLog, error) {
	for _, l := range s.m.priceLogs {
		if l.ID == id {
			return l, nil
		}
	}
	return nil, domain.ErrNotFound
}

func (s memPriceLogs) GetByNfceLine(_ context.Context, nfceID int64, line int) (*domain.PriceLog, error) {
	for _, l := range s.m.priceLogs {
		if l.NfceID == nfceID && l.Line == line {
			return l, nil
		}
	}
	return nil, domain.ErrNotFound
}

// memSynonyms implements domain.CommoditySynonymStore.
type memSynonyms struct{ m *memStores }

func (s memSynonyms) Create(_ context.Context, c *domain.CommoditySynonym) error {
	m := s.m
	if _, ok := m.synonyms[c.Synonym]; ok {
		return domain.ErrConflict
	}
	m.nextSynID++
	c.ID = m.nextSynID
	m.synonyms[c.Synonym] = c
	return nil
}

func (s memSynonyms) Canonicalize(_ context.Context, stem string) (*domain.CommoditySynonym, error) {
	if c, ok := s.m.synonyms[stem]; ok {
		return c, nil
	}
	return nil, domain.ErrNotFound
}

// ProductStore accessors for the service adapter.
func (m *memStores) Products() domain.ProductStore         { return memProducts{m} }
func (m *memStores) Previews() domain.PreviewStore         { return memPreviews{m} }
func (m *memStores) Supermarkets() domain.SupermarketStore { return memSupermarkets{m} }
func (m *memStores) Nfces() domain.NfceStore               { return memNfces{m} }
func (m *memStores) PriceLogs() domain.PriceLogStore       { return memPriceLogs{m} }
func (m *memStores) CommoditySynonyms() domain.CommoditySynonymStore {
	return memSynonyms{m}
}

func priceLogKey(nfceID int64, line int) string {
	return strconv.FormatInt(nfceID, 10) + ":" + strconv.Itoa(line)
}

func strPtr(s string) *string { return &s }

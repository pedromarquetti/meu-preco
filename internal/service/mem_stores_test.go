package service

import (
	"context"
	"errors"
	"sort"
	"strconv"
	"strings"
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

	users    map[string]*domain.User // by email
	byUserID map[int64]*domain.User
	nextUser int64

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
		users:        map[string]*domain.User{},
		byUserID:     map[int64]*domain.User{},
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
	users        map[string]*domain.User
	byUserID     map[int64]*domain.User
	nextUser     int64
	priceLogs    []*domain.PriceLog
	logKeys      map[string]bool
	nextLogID    int64
	synonyms     map[string]*domain.CommoditySynonym
	nextSynID    int64
}

func (m *memStores) snapshot() memSnapshot {
	return memSnapshot{
		products:     deepCopyMap(m.products),
		byGtin:       deepCopyMap(m.byGtin),
		byProductID:  deepCopyMap(m.byProductID),
		nextProdID:   m.nextProdID,
		previews:     deepCopyMap(m.previews),
		previewKey:   copyMap(m.previewKey),
		nextPrevID:   m.nextPrevID,
		supermarkets: deepCopyMap(m.supermarkets),
		nextShopID:   m.nextShopID,
		nfces:        deepCopyMap(m.nfces),
		nextNfceID:   m.nextNfceID,
		users:        deepCopyMap(m.users),
		byUserID:     deepCopyMap(m.byUserID),
		nextUser:     m.nextUser,
		priceLogs:    copyPriceLogs(m.priceLogs),
		logKeys:      copyMap(m.logKeys),
		nextLogID:    m.nextLogID,
		synonyms:     deepCopyMap(m.synonyms),
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

// deepCopyMap snapshots the pointed-to values too, so a failed Tx rollback
// undoes in-place mutations (Update, SetProduct, RePoint*) and not just row
// additions/deletions — mirroring a real database transaction.
func deepCopyMap[M ~map[K]*V, K comparable, V any](src M) M {
	dst := make(M, len(src))
	for k, v := range src {
		cp := *v
		dst[k] = &cp
	}
	return dst
}

func copyPriceLogs(src []*domain.PriceLog) []*domain.PriceLog {
	dst := make([]*domain.PriceLog, len(src))
	for i, v := range src {
		cp := *v
		dst[i] = &cp
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
	m.users = s.users
	m.byUserID = s.byUserID
	m.nextUser = s.nextUser
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

func (s memProducts) SearchFiltered(_ context.Context, f domain.ProductFilter, limit, offset int) ([]domain.Product, int64, error) {
	m := s.m
	var out []*domain.Product
	for _, p := range m.products {
		if p.NeedsReview {
			continue
		}
		if f.Query != "" && !containsFold(p.Name, f.Query) &&
			!containsFold(derefOr(p.Brand, ""), f.Query) &&
			!containsFold(derefOr(p.CanonicalStem, ""), f.Query) {
			continue
		}
		if f.GTIN != "" {
			if p.GTIN == nil || !strings.HasPrefix(strings.TrimLeft(*p.GTIN, "0"), f.GTIN) {
				continue
			}
		}
		if f.Unit != "" && p.Unit != f.Unit {
			continue
		}
		if f.Brand != "" && !containsFold(p.Name, f.Brand) && !containsFold(derefOr(p.Brand, ""), f.Brand) {
			continue
		}
		out = append(out, p)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	total := int64(len(out))
	start, end := pageBounds(offset, limit, len(out))
	return copyProducts(out[start:end]), total, nil
}

func (s memProducts) Update(_ context.Context, id int64, patch domain.ProductPatch) error {
	p, ok := s.m.byProductID[id]
	if !ok {
		return domain.ErrNotFound
	}
	if patch.Name != nil {
		p.Name = *patch.Name
	}
	if patch.Brand != nil {
		if *patch.Brand == "" {
			p.Brand = nil
		} else {
			p.Brand = patch.Brand
		}
	}
	if patch.Unit != nil {
		p.Unit = *patch.Unit
	}
	return nil
}

func (s memProducts) Delete(_ context.Context, id int64) error {
	p, ok := s.m.byProductID[id]
	if !ok {
		return domain.ErrNotFound
	}
	delete(s.m.byProductID, id)
	delete(s.m.products, p.ProductKey)
	if p.GTIN != nil {
		delete(s.m.byGtin, *p.GTIN)
	}
	return nil
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

func (s memPreviews) DeleteByAccessKey(_ context.Context, accessKey string) (int64, error) {
	m := s.m
	var n int64
	for id, p := range m.previews {
		if p.AccessKey == accessKey {
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

func (s memSupermarkets) List(_ context.Context, limit, offset int) ([]domain.Supermarket, int64, error) {
	var out []*domain.Supermarket
	for _, sm := range s.m.supermarkets {
		out = append(out, sm)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].LegalName < out[j].LegalName })
	total := int64(len(out))
	start, end := pageBounds(offset, limit, len(out))
	var page []domain.Supermarket
	for _, sm := range out[start:end] {
		page = append(page, *sm)
	}
	return page, total, nil
}

func (s memSupermarkets) RePointNfces(_ context.Context, fromID, toID int64) (int64, error) {
	if fromID == toID {
		return 0, nil
	}
	var to *domain.Supermarket
	for _, sm := range s.m.supermarkets {
		if sm.ID == toID {
			to = sm
			break
		}
	}
	if to == nil {
		return 0, domain.ErrNotFound
	}
	var moved int64
	for _, n := range s.m.nfces {
		if n.Supermarket.ID == fromID {
			n.Supermarket = *to
			moved++
		}
	}
	return moved, nil
}

func (s memSupermarkets) CountNfces(_ context.Context, id int64) (int64, error) {
	var n int64
	for _, nf := range s.m.nfces {
		if nf.Supermarket.ID == id {
			n++
		}
	}
	return n, nil
}

func (s memSupermarkets) Delete(_ context.Context, id int64) error {
	for cnpj, sm := range s.m.supermarkets {
		if sm.ID == id {
			delete(s.m.supermarkets, cnpj)
			return nil
		}
	}
	return domain.ErrNotFound
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

func (s memNfces) Delete(_ context.Context, id int64) error {
	for key, n := range s.m.nfces {
		if n.ID == id {
			delete(s.m.nfces, key)
			return nil
		}
	}
	return domain.ErrNotFound
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

func (s memPriceLogs) HistoryByProduct(_ context.Context, productID int64, from, to *time.Time) ([]domain.PricePoint, error) {
	m := s.m
	var out []domain.PricePoint
	for _, l := range m.priceLogs {
		if l.ProductID == nil || *l.ProductID != productID {
			continue
		}
		nf := nfceByID(m, l.NfceID)
		if nf == nil {
			continue
		}
		if from != nil && (nf.IssuedAt == nil || nf.IssuedAt.Before(*from)) {
			continue
		}
		if to != nil && (nf.IssuedAt == nil || nf.IssuedAt.After(*to)) {
			continue
		}
		out = append(out, domain.PricePoint{
			PriceLogID:     l.ID,
			NfceID:         l.NfceID,
			AccessKey:      nf.AccessKey,
			SupermarketID:  nf.Supermarket.ID,
			StoreCNPJ:      nf.Supermarket.CNPJ,
			StoreName:      nf.Supermarket.LegalName,
			Line:           l.Line,
			Qty:            l.Qty,
			Unit:           l.Unit,
			NormalizedUnit: l.NormalizedUnit,
			PricePerUnit:   l.PricePerUnit,
			UnitPrice:      l.UnitPrice,
			TotalPrice:     l.TotalPrice,
			IssuedAt:       nf.IssuedAt,
			LoggedAt:       l.CreatedAt,
		})
	}
	sort.SliceStable(out, func(i, j int) bool {
		ti := issuedAtOr(out[i])
		tj := issuedAtOr(out[j])
		if !ti.Equal(tj) {
			return ti.Before(tj)
		}
		if !out[i].LoggedAt.Equal(out[j].LoggedAt) {
			return out[i].LoggedAt.Before(out[j].LoggedAt)
		}
		return out[i].PriceLogID < out[j].PriceLogID
	})
	return out, nil
}

func (s memPriceLogs) LatestBySupermarket(_ context.Context, supermarketID int64, limit, offset int) ([]domain.SupermarketProduct, int64, error) {
	m := s.m
	best := map[int64]*domain.PriceLog{}
	for _, l := range m.priceLogs {
		if l.ProductID == nil || l.NeedsReview {
			continue
		}
		nf := nfceByID(m, l.NfceID)
		if nf == nil || nf.Supermarket.ID != supermarketID {
			continue
		}
		p, ok := m.byProductID[*l.ProductID]
		if !ok || p.NeedsReview {
			continue
		}
		if cur, seen := best[*l.ProductID]; !seen || stamp(l, nf).After(stamp(cur, nfceByID(m, cur.NfceID))) {
			best[*l.ProductID] = l
		}
	}
	keys := make([]int64, 0, len(best))
	for k := range best {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		return m.byProductID[keys[i]].Name < m.byProductID[keys[j]].Name
	})
	start, end := pageBounds(offset, limit, len(keys))
	var out []domain.SupermarketProduct
	for _, pid := range keys[start:end] {
		l := best[pid]
		nf := nfceByID(m, l.NfceID)
		out = append(out, domain.SupermarketProduct{
			Product:        *m.byProductID[pid],
			PriceLogID:     l.ID,
			Qty:            l.Qty,
			NeedsReview:    false,
			NormalizedUnit: l.NormalizedUnit,
			PricePerUnit:   l.PricePerUnit,
			UnitPrice:      l.UnitPrice,
			TotalPrice:     l.TotalPrice,
			LastIssuedAt:   nf.IssuedAt,
			LastLoggedAt:   l.CreatedAt,
		})
	}
	return out, int64(len(keys)), nil
}

func (s memPriceLogs) NeedsReview(_ context.Context, limit, offset int) ([]domain.ReviewItem, int64, error) {
	m := s.m
	var rows []*domain.PriceLog
	for _, l := range m.priceLogs {
		if l.NeedsReview {
			rows = append(rows, l)
		}
	}
	sort.SliceStable(rows, func(i, j int) bool {
		if !rows[i].CreatedAt.Equal(rows[j].CreatedAt) {
			return rows[i].CreatedAt.Before(rows[j].CreatedAt)
		}
		return rows[i].ID < rows[j].ID
	})
	total := int64(len(rows))
	start, end := pageBounds(offset, limit, len(rows))
	var page []domain.ReviewItem
	for _, l := range rows[start:end] {
		nf := nfceByID(m, l.NfceID)
		cnpjBase, storeName, accessKey := "", "", ""
		if nf != nil {
			cnpjBase = safeLeft(nf.Supermarket.CNPJ, 8)
			storeName = nf.Supermarket.LegalName
			accessKey = nf.AccessKey
		}
		page = append(page, domain.ReviewItem{
			PriceLogID:    l.ID,
			NfceID:        l.NfceID,
			AccessKey:     accessKey,
			Line:          l.Line,
			Description:   l.DescriptionSnapshot,
			StoreCode:     l.StoreCode,
			CNPJBase:      cnpjBase,
			StoreName:     storeName,
			Unit:          l.Unit,
			Qty:           l.Qty,
			PricePerUnit:  l.PricePerUnit,
			UnitPrice:     l.UnitPrice,
			TotalPrice:    l.TotalPrice,
			SuggestedStem: "",
			CreatedAt:     l.CreatedAt,
		})
	}
	return page, total, nil
}

func (s memPriceLogs) RePointProduct(_ context.Context, fromID, toID int64) (int64, error) {
	var n int64
	for _, l := range s.m.priceLogs {
		if l.ProductID != nil && *l.ProductID == fromID {
			l.ProductID = &toID
			n++
		}
	}
	return n, nil
}

func (s memPriceLogs) SetProduct(_ context.Context, priceLogID, productID int64, resolvedBy int64, note string) error {
	for _, l := range s.m.priceLogs {
		if l.ID == priceLogID {
			l.ProductID = &productID
			l.NeedsReview = false
			l.ResolvedBy = &resolvedBy
			l.ResolutionNote = &note
			return nil
		}
	}
	return domain.ErrNotFound
}

func (s memPriceLogs) DeleteByNfceID(_ context.Context, nfceID int64) (int64, error) {
	var n int64
	kept := s.m.priceLogs[:0]
	for _, l := range s.m.priceLogs {
		if l.NfceID == nfceID {
			delete(s.m.logKeys, priceLogKey(l.NfceID, l.Line))
			n++
			continue
		}
		kept = append(kept, l)
	}
	s.m.priceLogs = kept
	return n, nil
}

func (s memPriceLogs) DeleteByProduct(_ context.Context, productID int64) (int64, error) {
	var n int64
	kept := s.m.priceLogs[:0]
	for _, l := range s.m.priceLogs {
		if l.ProductID != nil && *l.ProductID == productID {
			delete(s.m.logKeys, priceLogKey(l.NfceID, l.Line))
			n++
			continue
		}
		kept = append(kept, l)
	}
	s.m.priceLogs = kept
	return n, nil
}

func (s memPriceLogs) CountByProduct(_ context.Context, productID int64) (int64, error) {
	var n int64
	for _, l := range s.m.priceLogs {
		if l.ProductID != nil && *l.ProductID == productID {
			n++
		}
	}
	return n, nil
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

// memUsers implements domain.UserStore.
type memUsers struct{ m *memStores }

func (s memUsers) Create(_ context.Context, u *domain.User) error {
	m := s.m
	if _, ok := m.users[u.Email]; ok {
		return domain.ErrConflict
	}
	m.nextUser++
	u.ID = m.nextUser
	u.CreatedAt = time.Now()
	m.users[u.Email] = u
	m.byUserID[u.ID] = u
	return nil
}

func (s memUsers) GetByID(_ context.Context, id int64) (*domain.User, error) {
	if u, ok := s.m.byUserID[id]; ok {
		return u, nil
	}
	return nil, domain.ErrNotFound
}

func (s memUsers) GetByEmail(_ context.Context, email string) (*domain.User, error) {
	if u, ok := s.m.users[email]; ok {
		return u, nil
	}
	return nil, domain.ErrNotFound
}

func (s memUsers) CountAdmin(context.Context) (int64, error) {
	var n int64
	for _, u := range s.m.users {
		if u.Role == "admin" {
			n++
		}
	}
	return n, nil
}

func (s memUsers) List(_ context.Context, limit, offset int) ([]domain.User, int64, error) {
	var out []*domain.User
	for _, u := range s.m.users {
		out = append(out, u)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if !out[i].CreatedAt.Equal(out[j].CreatedAt) {
			return out[i].CreatedAt.After(out[j].CreatedAt)
		}
		return out[i].ID > out[j].ID
	})
	total := int64(len(out))
	start, end := pageBounds(offset, limit, len(out))
	var page []domain.User
	for _, u := range out[start:end] {
		page = append(page, *u)
	}
	return page, total, nil
}

func (s memUsers) UpdateRole(_ context.Context, id int64, role string) error {
	u, ok := s.m.byUserID[id]
	if !ok {
		return domain.ErrNotFound
	}
	u.Role = role
	return nil
}

// ProductStore accessors for the service adapter.
func (m *memStores) Products() domain.ProductStore         { return memProducts{m} }
func (m *memStores) Previews() domain.PreviewStore         { return memPreviews{m} }
func (m *memStores) Supermarkets() domain.SupermarketStore { return memSupermarkets{m} }
func (m *memStores) Nfces() domain.NfceStore               { return memNfces{m} }
func (m *memStores) PriceLogs() domain.PriceLogStore       { return memPriceLogs{m} }
func (m *memStores) Users() domain.UserStore               { return memUsers{m} }
func (m *memStores) CommoditySynonyms() domain.CommoditySynonymStore {
	return memSynonyms{m}
}

func nfceByID(m *memStores, id int64) *domain.Nfce {
	for _, n := range m.nfces {
		if n.ID == id {
			return n
		}
	}
	return nil
}

func issuedAtOr(p domain.PricePoint) time.Time {
	if p.IssuedAt != nil {
		return *p.IssuedAt
	}
	return p.LoggedAt
}

func stamp(l *domain.PriceLog, nf *domain.Nfce) time.Time {
	if nf != nil && nf.IssuedAt != nil {
		return *nf.IssuedAt
	}
	return l.CreatedAt
}

func pageBounds(offset, limit, n int) (int, int) {
	if offset < 0 {
		offset = 0
	}
	if offset > n {
		offset = n
	}
	end := offset + limit
	if limit <= 0 || end > n {
		end = n
	}
	return offset, end
}

func copyProducts(in []*domain.Product) []domain.Product {
	var out []domain.Product
	for _, p := range in {
		out = append(out, *p)
	}
	return out
}

func containsFold(haystack, needle string) bool {
	return strings.Contains(strings.ToLower(haystack), strings.ToLower(needle))
}

func derefOr(s *string, fallback string) string {
	if s == nil {
		return fallback
	}
	return *s
}

func safeLeft(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}

func priceLogKey(nfceID int64, line int) string {
	return strconv.FormatInt(nfceID, 10) + ":" + strconv.Itoa(line)
}

func strPtr(s string) *string { return &s }

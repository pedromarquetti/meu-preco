package httpapi

import (
	"context"
	"strconv"
	"time"

	"meupreco/internal/domain"
)

// stubStores is a minimal in-memory domain.Stores for the /nfce unit tests:
// enough previews storage to serve the draft replay and submit error paths,
// plus a catalog that always answers "unknown" so plan building resolves
// GTIN-less lines to uid_uncertain without a product master. Anything else
// (the commit transaction, nfces, price_logs) panics on purpose — tests that
// need real behavior use the postgres-backed ingestionRouter.
type stubStores struct {
	domain.Stores
	previews domain.PreviewStore
	products domain.ProductStore
	synonyms domain.CommoditySynonymStore
}

func newStubStores() *stubStores {
	return &stubStores{
		previews: newMemPreviewStore(),
		products: &nilProductStore{},
		synonyms: &nilSynonymStore{},
	}
}

func (s *stubStores) Previews() domain.PreviewStore {
	return s.previews
}

func (s *stubStores) Products() domain.ProductStore {
	return s.products
}

func (s *stubStores) CommoditySynonyms() domain.CommoditySynonymStore {
	return s.synonyms
}

// Users/RefreshTokens/Ping are overridden so the embedded nil Stores interface
// never gets method-called (which would panic); the /nfce tests never reach
// the auth handlers, so nil identity stores are safe here.
func (s *stubStores) Users() domain.UserStore                 { return nil }
func (s *stubStores) RefreshTokens() domain.RefreshTokenStore { return nil }
func (s *stubStores) Ping(context.Context) error              { return nil }

// memPreviewStore mirrors the pg previews semantics the /nfce unit paths rely
// on: at most one pending row per (user, access_key).
type memPreviewStore struct {
	next    int64
	rows    map[int64]*domain.Preview
	pending map[string]int64
}

func newMemPreviewStore() *memPreviewStore {
	return &memPreviewStore{rows: map[int64]*domain.Preview{}, pending: map[string]int64{}}
}

func (m *memPreviewStore) pendingKey(userID int64, accessKey string) string {
	return strconv.FormatInt(userID, 10) + ":" + accessKey
}

func (m *memPreviewStore) Create(_ context.Context, p *domain.Preview) error {
	if p.Status == "pending" {
		if _, ok := m.pending[m.pendingKey(p.UserID, p.AccessKey)]; ok {
			return domain.ErrConflict
		}
	}
	m.next++
	p.ID = m.next
	m.rows[p.ID] = p
	if p.Status == "pending" {
		m.pending[m.pendingKey(p.UserID, p.AccessKey)] = p.ID
	}
	return nil
}

func (m *memPreviewStore) GetByID(_ context.Context, id int64) (*domain.Preview, error) {
	if p, ok := m.rows[id]; ok {
		return p, nil
	}
	return nil, domain.ErrNotFound
}

func (m *memPreviewStore) GetPendingByUserAccessKey(_ context.Context, userID int64, accessKey string) (*domain.Preview, error) {
	if id, ok := m.pending[m.pendingKey(userID, accessKey)]; ok {
		return m.rows[id], nil
	}
	return nil, domain.ErrNotFound
}

func (m *memPreviewStore) MarkCommitted(_ context.Context, id int64) error {
	p, ok := m.rows[id]
	if !ok {
		return domain.ErrNotFound
	}
	if p.Status == "committed" {
		return domain.ErrConflict
	}
	p.Status = "committed"
	delete(m.pending, m.pendingKey(p.UserID, p.AccessKey))
	return nil
}

func (m *memPreviewStore) PurgeExpired(_ context.Context) (int64, error) {
	now := time.Now()
	var n int64
	for id, p := range m.rows {
		if p.TTL.Before(now) {
			delete(m.rows, id)
			delete(m.pending, m.pendingKey(p.UserID, p.AccessKey))
			n++
		}
	}
	return n, nil
}

func (m *memPreviewStore) DeleteByAccessKey(_ context.Context, accessKey string) (int64, error) {
	var n int64
	for id, p := range m.rows {
		if p.AccessKey == accessKey {
			delete(m.rows, id)
			delete(m.pending, m.pendingKey(p.UserID, p.AccessKey))
			n++
		}
	}
	return n, nil
}

// nilProductStore is a product master that knows nothing.
type nilProductStore struct{}

func (*nilProductStore) Create(context.Context, *domain.Product) error { return nil }
func (*nilProductStore) GetByID(context.Context, int64) (*domain.Product, error) {
	return nil, domain.ErrNotFound
}
func (*nilProductStore) GetByKey(context.Context, string) (*domain.Product, error) {
	return nil, domain.ErrNotFound
}
func (*nilProductStore) GetByGTIN(context.Context, string) (*domain.Product, error) {
	return nil, domain.ErrNotFound
}
func (*nilProductStore) Search(context.Context, string, int, int) ([]domain.Product, error) {
	return nil, nil
}
func (*nilProductStore) UpsertMany(context.Context, []*domain.Product) (int, error) { return 0, nil }
func (*nilProductStore) NearestMatch(context.Context, string, float64) (*domain.Product, float64, error) {
	return nil, 0, domain.ErrNotFound
}
func (*nilProductStore) SearchFiltered(context.Context, domain.ProductFilter, int, int) ([]domain.Product, int64, error) {
	return nil, 0, nil
}
func (*nilProductStore) Update(context.Context, int64, domain.ProductPatch) error {
	return domain.ErrNotFound
}
func (*nilProductStore) Delete(context.Context, int64) error { return domain.ErrNotFound }

// nilSynonymStore is a produce taxonomy that knows nothing.
type nilSynonymStore struct{}

func (*nilSynonymStore) Create(context.Context, *domain.CommoditySynonym) error { return nil }
func (*nilSynonymStore) Canonicalize(context.Context, string) (*domain.CommoditySynonym, error) {
	return nil, domain.ErrNotFound
}

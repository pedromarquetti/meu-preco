package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"meupreco/internal/auth"
	"meupreco/internal/config"
	"meupreco/internal/domain"
)

const testJWTSecret = "unit-test-secret-with-at-least-32-characters"

// memUserStore is a thread-safe in-memory domain.UserStore for auth handler
// tests (mirrors the pg constraint: duplicate email -> ErrConflict).
type memUserStore struct {
	mu      sync.Mutex
	byID    map[int64]*domain.User
	byEmail map[string]*domain.User
	nextID  int64
}

func newMemUserStore() *memUserStore {
	return &memUserStore{byID: map[int64]*domain.User{}, byEmail: map[string]*domain.User{}}
}

func (s *memUserStore) Create(_ context.Context, u *domain.User) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.byEmail[u.Email]; ok {
		return domain.ErrConflict
	}
	if u.Name != "" {
		for _, existing := range s.byID {
			if existing.Name == u.Name {
				return domain.ErrConflict
			}
		}
	}
	s.nextID++
	u.ID = s.nextID
	u.CreatedAt = time.Now()
	s.byID[u.ID] = u
	s.byEmail[u.Email] = u
	return nil
}

func (s *memUserStore) GetByID(_ context.Context, id int64) (*domain.User, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if u, ok := s.byID[id]; ok {
		return u, nil
	}
	return nil, domain.ErrNotFound
}

func (s *memUserStore) GetByEmail(_ context.Context, email string) (*domain.User, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if u, ok := s.byEmail[email]; ok {
		return u, nil
	}
	return nil, domain.ErrNotFound
}

func (s *memUserStore) GetByLogin(_ context.Context, login string) (*domain.User, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if u, ok := s.byEmail[strings.ToLower(login)]; ok {
		return u, nil
	}
	for _, u := range s.byID {
		if u.Name == login {
			return u, nil
		}
	}
	return nil, domain.ErrNotFound
}

func (s *memUserStore) CountAdmin(context.Context) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return int64(len(s.byID)), nil
}

func (s *memUserStore) List(_ context.Context, limit, offset int) ([]domain.User, int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	users := make([]domain.User, 0, len(s.byID))
	for _, u := range s.byID {
		users = append(users, *u)
	}
	// newest first (mirrors the pg ORDER BY created_at DESC, id DESC)
	for i, j := 0, len(users)-1; i < j; i, j = i+1, j-1 {
		users[i], users[j] = users[j], users[i]
	}
	total := int64(len(users))
	end := offset + limit
	if limit <= 0 || offset < 0 {
		return nil, 0, nil
	}
	if offset >= len(users) {
		return []domain.User{}, total, nil
	}
	if end > len(users) {
		end = len(users)
	}
	return users[offset:end], total, nil
}

func (s *memUserStore) UpdateRole(_ context.Context, id int64, role string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	u, ok := s.byID[id]
	if !ok {
		return domain.ErrNotFound
	}
	u.Role = role
	return nil
}

// memRefreshStore is an in-memory domain.RefreshTokenStore mirroring the pg
// Rotate semantics (unknown hash -> ErrNotFound, already-revoked -> ErrConflict).
type memRefreshStore struct {
	mu     sync.Mutex
	byHash map[string]*domain.RefreshToken
	nextID int64
}

func newMemRefreshStore() *memRefreshStore {
	return &memRefreshStore{byHash: map[string]*domain.RefreshToken{}}
}

func (s *memRefreshStore) Create(_ context.Context, t *domain.RefreshToken) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.byHash[t.TokenHash]; ok {
		return domain.ErrConflict
	}
	s.nextID++
	t.ID = s.nextID
	t.CreatedAt = time.Now()
	s.byHash[t.TokenHash] = t
	return nil
}

func (s *memRefreshStore) Rotate(_ context.Context, oldHash string, next *domain.RefreshToken) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	row, ok := s.byHash[oldHash]
	if !ok {
		return domain.ErrNotFound
	}
	if row.Revoked {
		return domain.ErrConflict
	}
	row.Revoked = true
	s.nextID++
	next.ID = s.nextID
	next.CreatedAt = time.Now()
	s.byHash[next.TokenHash] = next
	return nil
}

func (s *memRefreshStore) RevokeFamily(_ context.Context, familyID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, t := range s.byHash {
		if t.FamilyID == familyID {
			t.Revoked = true
		}
	}
	return nil
}

// authTestEnv wires a router with in-memory stores and a seeded (or empty)
// user database.
func authTestEnv(t *testing.T, opts ...func(*config.Config)) (http.Handler, *memUserStore, *memRefreshStore) {
	t.Helper()
	users := newMemUserStore()
	refresh := newMemRefreshStore()
	cfg := config.Config{
		CORSOrigins:   []string{"https://app.example.com"},
		JWTSecret:     testJWTSecret,
		JWTAccessTTL:  15 * time.Minute,
		JWTRefreshTTL: 30 * 24 * time.Hour,
		AdminEmail:    "admin@example.com",
		AdminPassword: "adm1n-password",
	}
	for _, apply := range opts {
		apply(&cfg)
	}
	h := New(cfg, slog.New(slog.NewTextHandler(io.Discard, nil)), &fakeStores{users: users, refreshTokens: refresh})
	return h, users, refresh
}

func doJSON(t *testing.T, h http.Handler, method, path, body string, bearer string) *httptest.ResponseRecorder {
	t.Helper()
	var rd io.Reader
	if body != "" {
		rd = strings.NewReader(body)
	}
	req := httptest.NewRequest(method, path, rd)
	req.Header.Set("Content-Type", "application/json")
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func decodeBody(t *testing.T, rec *httptest.ResponseRecorder, dst any) {
	t.Helper()
	if err := json.Unmarshal(rec.Body.Bytes(), dst); err != nil {
		t.Fatalf("unmarshal %q: %v", rec.Body.String(), err)
	}
}

type tokenPair struct {
	AccessToken  string   `json:"access_token"`
	RefreshToken string   `json:"refresh_token"`
	User         userView `json:"user"`
}

type errEnvelope struct {
	Error struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

func TestRegisterLoginMeRefresh(t *testing.T) {
	h, _, _ := authTestEnv(t)
	email := "alice@example.com"
	name := "Alice"

	rec := doJSON(t, h, http.MethodPost, "/api/v1/auth/register",
		`{"name":"  Alice ","email":"  ALICE@example.COM ","password":"a-really-good-password"}`, "")
	if rec.Code != http.StatusCreated {
		t.Fatalf("register: status=%d body=%s", rec.Code, rec.Body.String())
	}
	var reg struct {
		User userView `json:"user"`
	}
	decodeBody(t, rec, &reg)
	if reg.User.Name != name || reg.User.Email != email || reg.User.Role != "user" || reg.User.ID == 0 {
		t.Errorf("register user = %+v", reg.User)
	}
	if reg.User.CreatedAt.IsZero() {
		t.Errorf("register user created_at is zero")
	}
	assertNoSecretLeak(t, rec.Body.Bytes(), "a-really-good-password")

	// Login by email through the canonical `login` key.
	rec = doJSON(t, h, http.MethodPost, "/api/v1/auth/login",
		`{"login":"alice@example.com","password":"a-really-good-password"}`, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("login: status=%d body=%s", rec.Code, rec.Body.String())
	}
	var first tokenPair
	decodeBody(t, rec, &first)
	if first.AccessToken == "" || first.RefreshToken == "" || first.User.Email != email {
		t.Errorf("login pair = %+v", first)
	}
	assertNoSecretLeak(t, rec.Body.Bytes(), "a-really-good-password", "password_hash")

	// The same credentials work with the name as the login identifier.
	rec = doJSON(t, h, http.MethodPost, "/api/v1/auth/login",
		`{"login":"Alice","password":"a-really-good-password"}`, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("login by name: status=%d body=%s", rec.Code, rec.Body.String())
	}
	var byName tokenPair
	decodeBody(t, rec, &byName)
	if byName.User.Email != email {
		t.Errorf("login by name user = %+v", byName.User)
	}

	rec = doJSON(t, h, http.MethodGet, "/api/v1/auth/me", "", first.AccessToken)
	if rec.Code != http.StatusOK {
		t.Fatalf("me: status=%d body=%s", rec.Code, rec.Body.String())
	}
	var me struct {
		User userView `json:"user"`
	}
	decodeBody(t, rec, &me)
	if me.User.Name != "Alice" || me.User.Email != email || me.User.Role != "user" {
		t.Errorf("me user = %+v", me.User)
	}
	if me.User.CreatedAt.IsZero() {
		t.Errorf("me created_at is zero")
	}

	rec = doJSON(t, h, http.MethodPost, "/api/v1/auth/refresh",
		`{"refresh_token":"`+first.RefreshToken+`"}`, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("refresh: status=%d body=%s", rec.Code, rec.Body.String())
	}
	var second tokenPair
	decodeBody(t, rec, &second)
	if second.RefreshToken == "" || second.AccessToken == "" {
		t.Fatalf("reissued pair empty: %+v", second)
	}

	// The consumed refresh token is a reuse attempt -> 401. Family-wide
	// revocation also kills the just-rotated successor (the whole session is
	// dead once any token in it is replayed).
	rec = doJSON(t, h, http.MethodPost, "/api/v1/auth/refresh",
		`{"refresh_token":"`+first.RefreshToken+`"}`, "")
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("reuse: status=%d want 401 body=%s", rec.Code, rec.Body.String())
	}

	rec = doJSON(t, h, http.MethodPost, "/api/v1/auth/refresh",
		`{"refresh_token":"`+second.RefreshToken+`"}`, "")
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("rotated successor after reuse: status=%d want 401 (family revoked)", rec.Code)
	}
}

func TestRegisterDuplicateEmail(t *testing.T) {
	h, _, _ := authTestEnv(t)
	body := `{"name":"Dup","email":"dup@example.com","password":"long-enough-password"}`
	if rec := doJSON(t, h, http.MethodPost, "/api/v1/auth/register", body, ""); rec.Code != http.StatusCreated {
		t.Fatalf("first register status=%d", rec.Code)
	}
	rec := doJSON(t, h, http.MethodPost, "/api/v1/auth/register", body, "")
	if rec.Code != http.StatusConflict {
		t.Fatalf("duplicate status=%d want 409 body=%s", rec.Code, rec.Body.String())
	}
	var env errEnvelope
	decodeBody(t, rec, &env)
	if env.Error.Code != "user_exists" {
		t.Errorf("duplicate code = %q", env.Error.Code)
	}
}

func TestRegisterValidation(t *testing.T) {
	h, _, _ := authTestEnv(t)
	bad := []struct{ name, body string }{
		{"missing name", `{"email":"x@example.com","password":"long-enough!"}`},
		{"blank name", `{"name":"   ","email":"x@example.com","password":"long-enough!"}`},
		{"bad email", `{"name":"X","email":"not-an-email","password":"long-enough!"}`},
		{"short password", `{"name":"X","email":"x@example.com","password":"short"}`},
		{"malformed json", `{"name":`},
	}
	for _, tc := range bad {
		rec := doJSON(t, h, http.MethodPost, "/api/v1/auth/register", tc.body, "")
		if rec.Code != http.StatusBadRequest && rec.Code != http.StatusUnprocessableEntity {
			t.Errorf("%s: status=%d want 4xx body=%s", tc.name, rec.Code, rec.Body.String())
		}
	}
}

func TestLoginBadCredentials(t *testing.T) {
	h, _, _ := authTestEnv(t)
	doJSON(t, h, http.MethodPost, "/api/v1/auth/register",
		`{"name":"Bob","email":"bob@example.com","password":"long-enough-password"}`, "")

	for _, body := range []string{
		`{"email":"bob@example.com","password":"wrong-password-123"}`,
		`{"email":"nobody@example.com","password":"long-enough-password"}`,
		`{"login":"Bob","password":"wrong-password-123"}`,
		`{"login":"Nobody","password":"long-enough-password"}`,
	} {
		rec := doJSON(t, h, http.MethodPost, "/api/v1/auth/login", body, "")
		if rec.Code != http.StatusUnauthorized {
			t.Errorf("login %s: status=%d want 401", body, rec.Code)
		}
	}
}

func TestMeRequiresAuth(t *testing.T) {
	h, _, _ := authTestEnv(t)

	if rec := doJSON(t, h, http.MethodGet, "/api/v1/auth/me", "", ""); rec.Code != http.StatusUnauthorized {
		t.Errorf("no token: status=%d want 401", rec.Code)
	}
	if rec := doJSON(t, h, http.MethodGet, "/api/v1/auth/me", "", "garbage.token.value"); rec.Code != http.StatusUnauthorized {
		t.Errorf("garbage token: status=%d want 401", rec.Code)
	}
}

func TestAccessTokenExpiry(t *testing.T) {
	h, _, _ := authTestEnv(t, func(c *config.Config) { c.JWTAccessTTL = 5 * time.Second })
	doJSON(t, h, http.MethodPost, "/api/v1/auth/register",
		`{"name":"Exp","email":"exp@example.com","password":"long-enough-password"}`, "")
	rec := doJSON(t, h, http.MethodPost, "/api/v1/auth/login",
		`{"email":"exp@example.com","password":"long-enough-password"}`, "")
	var pair tokenPair
	decodeBody(t, rec, &pair)

	if rec := doJSON(t, h, http.MethodGet, "/api/v1/auth/me", "", pair.AccessToken); rec.Code != http.StatusOK {
		t.Fatalf("fresh token: status=%d", rec.Code)
	}

	time.Sleep(6 * time.Second)
	if rec := doJSON(t, h, http.MethodGet, "/api/v1/auth/me", "", pair.AccessToken); rec.Code != http.StatusUnauthorized {
		t.Fatalf("expired token: status=%d want 401", rec.Code)
	}
}

func TestAdminGate(t *testing.T) {
	h, users, _ := authTestEnv(t)
	ctx := context.Background()

	// Nobody without a token.
	if rec := doJSON(t, h, http.MethodGet, "/api/v1/admin/users", "", ""); rec.Code != http.StatusUnauthorized {
		t.Errorf("no token: status=%d want 401", rec.Code)
	}

	// Seed the admin the way cmd/server does at boot (mandatory env).
	hash, err := auth.HashPassword("adm1n-password")
	if err != nil {
		t.Fatal(err)
	}
	if err := users.Create(ctx, &domain.User{
		Name:         "Admin",
		Email:        "admin@example.com",
		PasswordHash: hash,
		Role:         "admin",
	}); err != nil {
		t.Fatalf("seed admin: %v", err)
	}

	adminLogin := doJSON(t, h, http.MethodPost, "/api/v1/auth/login",
		`{"email":"admin@example.com","password":"adm1n-password"}`, "")
	var adminPair tokenPair
	decodeBody(t, adminLogin, &adminPair)
	if adminPair.User.Role != "admin" {
		t.Fatalf("admin login role = %q", adminPair.User.Role)
	}
	if rec := doJSON(t, h, http.MethodGet, "/api/v1/admin/users", "", adminPair.AccessToken); rec.Code != http.StatusOK {
		t.Errorf("admin: status=%d want 200 body=%s", rec.Code, rec.Body.String())
	}

	// A regular user is forbidden.
	doJSON(t, h, http.MethodPost, "/api/v1/auth/register",
		`{"name":"Regular","email":"user@example.com","password":"long-enough-password"}`, "")
	userLogin := doJSON(t, h, http.MethodPost, "/api/v1/auth/login",
		`{"email":"user@example.com","password":"long-enough-password"}`, "")
	var userPair tokenPair
	decodeBody(t, userLogin, &userPair)
	if rec := doJSON(t, h, http.MethodGet, "/api/v1/admin/users", "", userPair.AccessToken); rec.Code != http.StatusForbidden {
		t.Errorf("non-admin: status=%d want 403", rec.Code)
	}
}

func assertNoSecretLeak(t *testing.T, body []byte, forbidden ...string) {
	t.Helper()
	for _, s := range forbidden {
		if s == "" {
			continue
		}
		if bytes.Contains(body, []byte(s)) {
			t.Errorf("response leaked secret %q: %s", s, body)
		}
	}
	if bytes.Contains(body, []byte("$2a$")) || bytes.Contains(body, []byte("$2b$")) {
		t.Errorf("response leaked a bcrypt hash: %s", body)
	}
}

package main

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"sync"
	"testing"

	"meupreco/internal/auth"
	"meupreco/internal/config"
	"meupreco/internal/domain"
)

// memUserStore is an in-memory domain.UserStore for seeding tests.
type memUserStore struct {
	mu   sync.Mutex
	byID map[int64]*domain.User
	next int64
}

func newMemUserStore() *memUserStore {
	return &memUserStore{byID: map[int64]*domain.User{}}
}

func (s *memUserStore) Create(_ context.Context, u *domain.User) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.next++
	u.ID = s.next
	s.byID[u.ID] = u
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
	for _, u := range s.byID {
		if u.Email == email {
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

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func adminCfg() config.Config {
	return config.Config{
		AdminEmail:    " OPS@EXAMPLE.COM ",
		AdminPassword: "a-deploy-admin-password",
	}
}

func TestSeedAdminEmptyTable(t *testing.T) {
	ctx := context.Background()
	users := newMemUserStore()

	if err := seedAdmin(ctx, discardLogger(), users, adminCfg()); err != nil {
		t.Fatalf("seed: %v", err)
	}

	got, err := users.GetByEmail(ctx, "ops@example.com")
	if err != nil {
		t.Fatalf("admin not created: %v", err)
	}
	if got.Role != "admin" {
		t.Errorf("role = %q, want admin", got.Role)
	}
	if !auth.CheckPassword(got.PasswordHash, "a-deploy-admin-password") {
		t.Error("stored hash does not match the admin password")
	}
	if got.Email != "ops@example.com" {
		t.Errorf("email = %q, want normalized lowercased", got.Email)
	}
}

func TestSeedAdminIdempotent(t *testing.T) {
	ctx := context.Background()
	users := newMemUserStore()

	if err := seedAdmin(ctx, discardLogger(), users, adminCfg()); err != nil {
		t.Fatalf("first seed: %v", err)
	}
	if err := seedAdmin(ctx, discardLogger(), users, adminCfg()); err != nil {
		t.Fatalf("second seed: %v", err)
	}
	n, err := users.CountAdmin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Errorf("user count = %d after two seeds, want 1", n)
	}
}

func TestSeedAdminSkipsWhenUsersExist(t *testing.T) {
	ctx := context.Background()
	users := newMemUserStore()

	u := &domain.User{Email: "alice@example.com", PasswordHash: "h", Role: "user"}
	if err := users.Create(ctx, u); err != nil {
		t.Fatal(err)
	}

	if err := seedAdmin(ctx, discardLogger(), users, adminCfg()); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if _, err := users.GetByEmail(ctx, "ops@example.com"); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("expected admin NOT created when users exist, got err=%v", err)
	}
}

func TestSeedAdminPropagatesStoreErrors(t *testing.T) {
	ctx := context.Background()

	failing := &failingUserStore{}
	if err := seedAdmin(ctx, discardLogger(), failing, adminCfg()); err == nil {
		t.Fatal("expected error from store, got nil")
	}
}

// failingUserStore fails Count to exercise the error path.
type failingUserStore struct{}

func (failingUserStore) Create(context.Context, *domain.User) error { return nil }
func (failingUserStore) GetByID(context.Context, int64) (*domain.User, error) {
	return nil, domain.ErrNotFound
}
func (failingUserStore) GetByEmail(context.Context, string) (*domain.User, error) {
	return nil, domain.ErrNotFound
}
func (failingUserStore) CountAdmin(context.Context) (int64, error) {
	return 0, errors.New("db down")
}

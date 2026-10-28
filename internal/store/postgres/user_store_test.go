package store

import (
	"context"
	"errors"
	"testing"

	"meupreco/internal/domain"
)

func TestUserStoreLifecycle(t *testing.T) {
	s := testStore
	truncate(t)
	ctx := context.Background()

	u := &domain.User{Name: "Ana", Email: "ana@example.com", PasswordHash: "hash", Role: "user"}
	if err := s.Users().Create(ctx, u); err != nil {
		t.Fatalf("create: %v", err)
	}
	if u.ID == 0 || u.CreatedAt.IsZero() {
		t.Fatalf("create did not populate id/created_at: %+v", u)
	}

	got, err := s.Users().GetByEmail(ctx, "ana@example.com")
	if err != nil {
		t.Fatalf("get by email: %v", err)
	}
	if got.ID != u.ID || got.Name != "Ana" || got.PasswordHash != "hash" || got.Role != "user" {
		t.Errorf("get by email = %+v", got)
	}

	byID, err := s.Users().GetByID(ctx, u.ID)
	if err != nil {
		t.Fatalf("get by id: %v", err)
	}
	if byID.Email != "ana@example.com" || byID.Name != "Ana" {
		t.Errorf("get by id = %+v", byID)
	}

	byEmail, err := s.Users().GetByLogin(ctx, "ana@example.com")
	if err != nil || byEmail.ID != u.ID {
		t.Fatalf("get by login(email) = %+v err=%v", byEmail, err)
	}
	byName, err := s.Users().GetByLogin(ctx, "Ana")
	if err != nil || byName.ID != u.ID {
		t.Fatalf("get by login(name) = %+v err=%v", byName, err)
	}
}

func TestUserStoreDuplicateEmail(t *testing.T) {
	s := testStore
	truncate(t)
	ctx := context.Background()

	if err := s.Users().Create(ctx, &domain.User{Name: "Dup", Email: "dup@example.com", PasswordHash: "h"}); err != nil {
		t.Fatalf("first create: %v", err)
	}
	err := s.Users().Create(ctx, &domain.User{Name: "Dup", Email: "dup@example.com", PasswordHash: "h2"})
	if !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("duplicate email error = %v, want ErrConflict", err)
	}
}

func TestUserStoreNotFound(t *testing.T) {
	s := testStore
	truncate(t)
	ctx := context.Background()

	if _, err := s.Users().GetByEmail(ctx, "nobody@example.com"); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("get missing = %v, want ErrNotFound", err)
	}
	if _, err := s.Users().GetByID(ctx, 999999); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("get missing id = %v, want ErrNotFound", err)
	}
}

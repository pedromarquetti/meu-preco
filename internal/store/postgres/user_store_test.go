package store

import (
	"context"
	"errors"
	"testing"

	"meupreco/internal/domain"
)

func TestUserStoreLifecycle(t *testing.T) {
	s := mustStore(t)
	truncate(t)
	ctx := context.Background()

	u := &domain.User{Email: "ana@example.com", PasswordHash: "hash", Role: "user"}
	if err := s.Users().Create(ctx, u); err != nil {
		t.Fatalf("create: %v", err)
	}
	if u.ID == 0 {
		t.Fatal("create did not set ID")
	}
	if u.Role != "user" {
		t.Errorf("role = %q, want user", u.Role)
	}

	got, err := s.Users().GetByEmail(ctx, "ana@example.com")
	if err != nil {
		t.Fatalf("get by email: %v", err)
	}
	if got.ID != u.ID || got.PasswordHash != "hash" {
		t.Errorf("get by email = %+v", got)
	}

	got, err = s.Users().GetByID(ctx, u.ID)
	if err != nil {
		t.Fatalf("get by id: %v", err)
	}
	if got.Email != "ana@example.com" {
		t.Errorf("email = %q", got.Email)
	}

	adm, err := s.Users().GetByID(ctx, u.ID)
	if err != nil {
		t.Fatalf("get admin: %v", err)
	}
	if adm.Role != "user" {
		t.Errorf("role = %q", adm.Role)
	}
}

func TestUserStoreDuplicateEmail(t *testing.T) {
	s := mustStore(t)
	truncate(t)
	ctx := context.Background()

	if err := s.Users().Create(ctx, &domain.User{Email: "dup@example.com", PasswordHash: "h"}); err != nil {
		t.Fatalf("first create: %v", err)
	}
	err := s.Users().Create(ctx, &domain.User{Email: "dup@example.com", PasswordHash: "h2"})
	if !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("duplicate email error = %v, want ErrConflict", err)
	}
}

func TestUserStoreNotFound(t *testing.T) {
	s := mustStore(t)
	truncate(t)
	ctx := context.Background()

	if _, err := s.Users().GetByEmail(ctx, "nobody@example.com"); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("get missing = %v, want ErrNotFound", err)
	}
	if _, err := s.Users().GetByID(ctx, 999999); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("get missing id = %v, want ErrNotFound", err)
	}
}

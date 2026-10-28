package store

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"meupreco/internal/domain"
)

func tokenRow(userID int64, family, hash string) *domain.RefreshToken {
	return &domain.RefreshToken{
		UserID:    userID,
		FamilyID:  family,
		TokenHash: hash,
		ExpiresAt: time.Now().Add(time.Hour),
	}
}

func TestRefreshTokenRotateConsumesOldHash(t *testing.T) {
	s := testStore
	truncate(t)
	ctx := context.Background()
	userID := seedUser(t, s).ID

	if err := s.RefreshTokens().Create(ctx, tokenRow(userID, "fam-1", "hash-1")); err != nil {
		t.Fatalf("create: %v", err)
	}

	// First rotation consumes hash-1 and inserts hash-2 in its family.
	err := s.RefreshTokens().Rotate(ctx, "hash-1", tokenRow(userID, "fam-1", "hash-2"))
	if err != nil {
		t.Fatalf("rotate: %v", err)
	}

	// Consumed hash must not rotate again.
	err = s.RefreshTokens().Rotate(ctx, "hash-1", tokenRow(userID, "fam-1", "hash-3"))
	if !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("reuse error = %v, want ErrConflict", err)
	}

	// The successor rotates fine.
	if err := s.RefreshTokens().Rotate(ctx, "hash-2", tokenRow(userID, "fam-1", "hash-4")); err != nil {
		t.Fatalf("rotate successor: %v", err)
	}
}

func TestRefreshTokenRotateUnknownHash(t *testing.T) {
	s := testStore
	truncate(t)
	ctx := context.Background()
	userID := seedUser(t, s).ID

	err := s.RefreshTokens().Rotate(ctx, "never-issued", tokenRow(userID, "fam-x", "hash-y"))
	if !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("rotate unknown = %v, want ErrNotFound", err)
	}
}

func TestRefreshTokenRevokeFamily(t *testing.T) {
	s := testStore
	truncate(t)
	ctx := context.Background()
	userID := seedUser(t, s).ID

	for _, h := range []string{"hf1", "hf2"} {
		if err := s.RefreshTokens().Create(ctx, tokenRow(userID, "fam-k", h)); err != nil {
			t.Fatalf("create %s: %v", h, err)
		}
	}

	if err := s.RefreshTokens().RevokeFamily(ctx, "fam-k"); err != nil {
		t.Fatalf("revoke family: %v", err)
	}
	err := s.RefreshTokens().Rotate(ctx, "hf1", tokenRow(userID, "fam-k", "hf3"))
	if !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("rotate revoked = %v, want ErrConflict", err)
	}
}

func TestRefreshTokenDuplicateHash(t *testing.T) {
	s := testStore
	truncate(t)
	ctx := context.Background()
	userID := seedUser(t, s).ID

	if err := s.RefreshTokens().Create(ctx, tokenRow(userID, "fam-a", "same-hash")); err != nil {
		t.Fatalf("first create: %v", err)
	}
	err := s.RefreshTokens().Create(ctx, tokenRow(userID, "fam-b", "same-hash"))
	if !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("duplicate hash = %v, want ErrConflict", err)
	}
}

// TestRefreshTokenConcurrentRotateOnlyOneWins proves the FOR UPDATE lock: two
// goroutines racing to rotate the same token must not both succeed.
func TestRefreshTokenConcurrentRotateOnlyOneWins(t *testing.T) {
	s := testStore
	truncate(t)
	ctx := context.Background()
	userID := seedUser(t, s).ID

	if err := s.RefreshTokens().Create(ctx, tokenRow(userID, "fam-r", "shared-hash")); err != nil {
		t.Fatalf("create: %v", err)
	}

	var wg sync.WaitGroup
	results := make(chan error, 2)
	for _, h := range []string{"next-a", "next-b"} {
		wg.Add(1)
		go func(hash string) {
			defer wg.Done()
			results <- s.RefreshTokens().Rotate(ctx, "shared-hash", tokenRow(userID, "fam-r", hash))
		}(h)
	}
	wg.Wait()
	close(results)

	ok, conflict := 0, 0
	for err := range results {
		switch {
		case err == nil:
			ok++
		case errors.Is(err, domain.ErrConflict):
			conflict++
		default:
			t.Fatalf("unexpected rotate error: %v", err)
		}
	}
	if ok != 1 || conflict != 1 {
		t.Errorf("rotate outcomes = %d ok / %d conflict, want 1/1", ok, conflict)
	}
}

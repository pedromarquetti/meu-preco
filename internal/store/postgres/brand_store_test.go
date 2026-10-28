package store

import (
	"context"
	"testing"
)

func TestCommodityBrandsSeeded(t *testing.T) {
	// The 000007 migration seeds the curated brand vocabulary; after the
	// TestMain reset the deli-cheese brands from the live receipts are present.
	s := testStore
	ctx := context.Background()

	brands, err := s.Brands().List(ctx)
	if err != nil {
		t.Fatalf("list brands: %v", err)
	}
	if len(brands) == 0 {
		t.Fatal("no brands seeded")
	}

	got := make(map[string]string, len(brands))
	for _, b := range brands {
		got[b.Brand] = b.Display
	}
	for key, want := range map[string]string{
		"tirolez": "Tirolez",
		"vigor":   "Vigor",
	} {
		if got[key] != want {
			t.Errorf("brand %q display = %q, want %q", key, got[key], want)
		}
	}
}

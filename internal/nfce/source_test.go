package nfce

import (
	"context"
	"errors"
	"testing"
	"time"

	"meupreco/internal/domain"
)

type fakeSource struct{ payload string }

func (f *fakeSource) Fetch(_ context.Context, _ AccessKey, payload string) ([]byte, error) {
	f.payload = payload
	return []byte("<fake/>"), nil
}

type fakeParser struct{}

func (fakeParser) Parse(_ context.Context, _ AccessKey, _ []byte) (*domain.Nfce, error) {
	return &domain.Nfce{AccessKey: "x", Number: "1"}, nil
}

func TestMultiResolverRoutesToSupportedUF(t *testing.T) {
	src := &fakeSource{}
	m := NewMultiResolver(map[string]*Resolver{"35": NewResolver(src, fakeParser{})})

	key, _ := ParseAccessKey(senateKey)
	n, err := m.Resolve(context.Background(), key, senateKey+"|3|1")
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if n == nil || n.Number != "1" {
		t.Errorf("receipt = %+v", n)
	}
	if src.payload != senateKey+"|3|1" {
		t.Errorf("source payload = %q, want the forwarded payload", src.payload)
	}
}

func TestMultiResolverRejectsUnsupportedUF(t *testing.T) {
	m := NewMultiResolver(map[string]*Resolver{"35": NewResolver(&fakeSource{}, fakeParser{})})
	key, _ := ParseAccessKey("31240112345000123456650010000002571123456785") // MG, registered but unsupported

	_, err := m.Resolve(context.Background(), key, "")
	if !errors.Is(err, ErrUnsupportedUF) {
		t.Fatalf("resolve error = %v, want ErrUnsupportedUF", err)
	}
}

func TestMultiResolverEmptyTable(t *testing.T) {
	m := NewMultiResolver(nil)
	key, _ := ParseAccessKey(senateKey)
	if _, err := m.Resolve(context.Background(), key, ""); !errors.Is(err, ErrUnsupportedUF) {
		t.Fatalf("resolve error = %v, want ErrUnsupportedUF", err)
	}
}

// countingSource counts portal fetches so tests can prove the cache
// short-circuits the source.
type countingSource struct {
	fetches int
}

func (c *countingSource) Fetch(_ context.Context, _ AccessKey, _ string) ([]byte, error) {
	c.fetches++
	return []byte("<cache/>"), nil
}

func TestResolverSolvedCacheSkipsFetch(t *testing.T) {
	src := &countingSource{}
	r := NewResolver(src, fakeParser{})
	key, _ := ParseAccessKey(senateKey)

	r.CacheSolved(key, []byte("<solved/>"))
	if n, err := r.Resolve(context.Background(), key, ""); err != nil || n == nil {
		t.Fatalf("resolve = %+v, %v", n, err)
	}
	if src.fetches != 0 {
		t.Errorf("source fetches = %d, want 0 (served from solved cache)", src.fetches)
	}

	// Without a solved entry the source is hit normally.
	r2 := NewResolver(src, fakeParser{})
	if _, err := r2.Resolve(context.Background(), key, ""); err != nil {
		t.Fatalf("resolve = %v", err)
	}
	if src.fetches != 1 {
		t.Errorf("source fetches = %d, want 1 after cache miss", src.fetches)
	}
}

func TestResolverSolvedCacheExpires(t *testing.T) {
	src := &countingSource{}
	r := NewResolver(src, fakeParser{})
	key, _ := ParseAccessKey(senateKey)

	r.ttl = -time.Minute // already-expired solved receipts
	r.CacheSolved(key, []byte("<solved/>"))

	if _, err := r.Resolve(context.Background(), key, ""); err != nil {
		t.Fatalf("resolve = %v", err)
	}
	if src.fetches != 1 {
		t.Errorf("source fetches = %d, want 1 (expired solved entry re-fetches)", src.fetches)
	}
}

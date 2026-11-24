package nfce

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
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

func TestMultiResolverSolvedCacheSkipsFetch(t *testing.T) {
	src := &countingSource{}
	m := NewMultiResolver(map[string]*Resolver{"35": NewResolver(src, fakeParser{})})
	key, _ := ParseAccessKey(senateKey)

	m.CacheSolved(key, []byte("<solved/>"))
	if n, err := m.Resolve(context.Background(), key, ""); err != nil || n == nil {
		t.Fatalf("resolve = %+v, %v", n, err)
	}
	if src.fetches != 0 {
		t.Errorf("source fetches = %d, want 0 (served through MultiResolver)", src.fetches)
	}
}

func TestMultiResolverSolvedCacheUnsupportedUFIsNoOp(t *testing.T) {
	m := NewMultiResolver(map[string]*Resolver{"35": NewResolver(&countingSource{}, fakeParser{})})
	key, _ := ParseAccessKey("31240112345000123456650010000002571123456785") // MG

	m.CacheSolved(key, []byte("<solved/>")) // must not panic

	if _, err := m.Resolve(context.Background(), key, ""); !errors.Is(err, ErrUnsupportedUF) {
		t.Fatalf("resolve error = %v, want ErrUnsupportedUF", err)
	}
}

func TestResolverSolvedCacheExpires(t *testing.T) {
	src := &countingSource{}
	r := NewResolver(src, fakeParser{})
	key, _ := ParseAccessKey(senateKey)

	r.solvedTTL = -time.Minute // already-expired solved receipts
	r.CacheSolved(key, []byte("<solved/>"))

	if _, err := r.Resolve(context.Background(), key, ""); err != nil {
		t.Fatalf("resolve = %v", err)
	}
	if src.fetches != 1 {
		t.Errorf("source fetches = %d, want 1 (expired solved entry re-fetches)", src.fetches)
	}
}

func TestResolverCachesRawReceiptWithinTTL(t *testing.T) {
	src := &countingSource{}
	r := NewResolver(src, fakeParser{})
	key, _ := ParseAccessKey(senateKey)

	for i := 0; i < 2; i++ {
		if _, err := r.Resolve(context.Background(), key, ""); err != nil {
			t.Fatalf("resolve %d: %v", i, err)
		}
	}
	if src.fetches != 1 {
		t.Errorf("source fetches = %d, want 1 (second resolve within TTL)", src.fetches)
	}
}

func TestResolverRawCacheExpires(t *testing.T) {
	src := &countingSource{}
	r := NewResolver(src, fakeParser{})
	key, _ := ParseAccessKey(senateKey)

	r.rawTTL = -time.Minute // every stored entry is already expired
	for i := 0; i < 2; i++ {
		if _, err := r.Resolve(context.Background(), key, ""); err != nil {
			t.Fatalf("resolve %d: %v", i, err)
		}
	}
	if src.fetches != 2 {
		t.Errorf("source fetches = %d, want 2 (expired raw entry re-fetches)", src.fetches)
	}
}

// gatedSource blocks its Fetch until the test releases it, so concurrent
// callers have a chance to join the in-flight request.
type gatedSource struct {
	fetches atomic.Int32
	started chan struct{}
	release chan struct{}
}

func newGatedSource() *gatedSource {
	return &gatedSource{started: make(chan struct{}, 1), release: make(chan struct{})}
}

func (s *gatedSource) Fetch(ctx context.Context, _ AccessKey, _ string) ([]byte, error) {
	s.fetches.Add(1)
	select {
	case s.started <- struct{}{}:
	default:
	}
	select {
	case <-s.release:
		return []byte("<gated/>"), nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func TestResolverCoalescesConcurrentSameKeyFetches(t *testing.T) {
	src := newGatedSource()
	r := NewResolver(src, fakeParser{})
	key, _ := ParseAccessKey(senateKey)

	const callers = 8
	var wg sync.WaitGroup
	errs := make(chan error, callers)
	ready := make(chan struct{}, callers)

	for i := 0; i < callers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ready <- struct{}{}
			_, err := r.Resolve(context.Background(), key, "")
			errs <- err
		}()
	}
	for i := 0; i < callers; i++ {
		<-ready
	}

	select {
	case <-src.started:
	case <-time.After(2 * time.Second):
		t.Fatal("source fetch did not start")
	}
	close(src.release)
	wg.Wait()
	close(errs)

	for err := range errs {
		if err != nil {
			t.Errorf("resolve: %v", err)
		}
	}
	if got := src.fetches.Load(); got != 1 {
		t.Errorf("source fetches = %d, want 1 for concurrent same-key resolves", got)
	}
}

// barrierSource waits for every expected Fetch to start before releasing them,
// proving different keys run independently.
type barrierSource struct {
	mu      sync.Mutex
	fetches int
	ready   chan struct{}
	release chan struct{}
}

func newBarrierSource(expected int) *barrierSource {
	return &barrierSource{ready: make(chan struct{}, expected), release: make(chan struct{})}
}

func (s *barrierSource) Fetch(ctx context.Context, _ AccessKey, _ string) ([]byte, error) {
	s.mu.Lock()
	s.fetches++
	s.mu.Unlock()

	s.ready <- struct{}{}
	select {
	case <-s.release:
		return []byte("<barrier/>"), nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (s *barrierSource) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.fetches
}

func TestResolverDoesNotCoalesceDifferentKeys(t *testing.T) {
	src := newBarrierSource(2)
	r := NewResolver(src, fakeParser{})

	keys := make([]AccessKey, 0, 2)
	for _, raw := range []string{senateKey, "31240112345000123456650010000002571123456785"} {
		key, err := ParseAccessKey(raw)
		if err != nil {
			t.Fatalf("parse key %s: %v", raw, err)
		}
		keys = append(keys, key)
	}

	var wg sync.WaitGroup
	errs := make(chan error, len(keys))
	for _, key := range keys {
		wg.Add(1)
		go func(key AccessKey) {
			defer wg.Done()
			_, err := r.Resolve(context.Background(), key, "")
			errs <- err
		}(key)
	}

	for i := 0; i < len(keys); i++ {
		select {
		case <-src.ready:
		case <-time.After(2 * time.Second):
			t.Fatal("different keys were coalesced into one fetch")
		}
	}
	close(src.release)
	wg.Wait()
	close(errs)

	for err := range errs {
		if err != nil {
			t.Errorf("resolve: %v", err)
		}
	}
	if got := src.count(); got != len(keys) {
		t.Errorf("source fetches = %d, want %d for different keys", got, len(keys))
	}
}

package nfce

import (
	"context"
	"fmt"
	"sync"
	"time"

	"meupreco/internal/domain"
)

// Source is the abstraction over data providers (SEFAZ-SP portal, other UFs,
// mock data). Implementations return raw page bytes for a given access key.
// payload, when non-empty, is the exact QR payload the consumer scanned and
// overrides the URL built from key alone (see ParseQRPayload).
type Source interface {
	Fetch(ctx context.Context, key AccessKey, payload string) ([]byte, error)
}

// Parser converts a raw DANFE payload into a normalized domain receipt.
type Parser interface {
	Parse(ctx context.Context, key AccessKey, raw []byte) (*domain.Nfce, error)
}

// Resolver pairs a Source with a Parser and provides the two-step
// fetch → parse flow. Raw DANFE bytes are cached by access key for a short
// TTL, so repeated drafts of the same receipt (including concurrent ones) do
// not amplify traffic against the portal. Concurrent fetches for the same key
// are coalesced: one caller performs the fetch and the others wait for it.
//
// A solver may also seed a receipt through CacheSolved after a captcha
// challenge. That entry lives longer than an ordinary fetch so the immediate
// draft fetch after a solve does not trigger the challenge again.
type Resolver struct {
	Source Source
	Parser Parser

	mu       sync.Mutex
	receipts map[string]receiptEntry
	inflight map[string]*receiptCall

	solvedTTL time.Duration
	rawTTL    time.Duration
}

// receiptEntry is a cached raw DANFE and when it expires.
type receiptEntry struct {
	raw []byte
	exp time.Time
}

// receiptCall is one in-flight fetch shared by concurrent callers for the same
// access key. raw/err are written before done is closed and only read after.
type receiptCall struct {
	done chan struct{}
	raw  []byte
	err  error
}

const (
	// solvedReceiptTTL keeps a solved receipt alive across the draft → commit
	// window (the pending preview itself lives PreviewTTL).
	solvedReceiptTTL = 20 * time.Minute
	// rawReceiptTTL is the short general cache: enough to collapse a burst of
	// drafts for the same receipt without serving stale prices later.
	rawReceiptTTL = 2 * time.Minute
)

// NewResolver creates a resolver from a source and parser.
func NewResolver(src Source, p Parser) *Resolver {
	return &Resolver{
		Source:    src,
		Parser:    p,
		receipts:  make(map[string]receiptEntry),
		inflight:  make(map[string]*receiptCall),
		solvedTTL: solvedReceiptTTL,
		rawTTL:    rawReceiptTTL,
	}
}

// CacheSolved stores raw DANFE bytes for key so the next Resolve for that key
// serves the receipt without another portal fetch (the captcha bridge).
func (r *Resolver) CacheSolved(key AccessKey, raw []byte) {
	if raw == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.sweepLocked()
	r.receipts[key.String()] = receiptEntry{raw: raw, exp: time.Now().Add(r.solvedTTL)}
}

// Resolve fetches the raw DANFE from the source and parses it with the
// configured parser. A cached receipt (ordinary or captcha-solved)
// short-circuits the fetch entirely — the parser still runs, so the returned
// document is fresh. Any error returned is already domain-typed
// (ErrInvalidKey, CaptchaError, ErrUnsupportedUF, etc.).
func (r *Resolver) Resolve(ctx context.Context, key AccessKey, payload string) (*domain.Nfce, error) {
	raw, err := r.raw(ctx, key, payload)
	if err != nil {
		return nil, err
	}
	return r.Parser.Parse(ctx, key, raw)
}

func (r *Resolver) raw(ctx context.Context, key AccessKey, payload string) ([]byte, error) {
	cacheKey := key.String()

	r.mu.Lock()
	r.sweepLocked()
	if e, ok := r.receipts[cacheKey]; ok && time.Now().Before(e.exp) {
		raw := e.raw
		r.mu.Unlock()
		return raw, nil
	}
	if call, ok := r.inflight[cacheKey]; ok {
		r.mu.Unlock()
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-call.done:
		}
		// A solved receipt may have been seeded while this caller waited for
		// the shared fetch; prefer it over the fetch result.
		r.mu.Lock()
		e, ok := r.receipts[cacheKey]
		r.mu.Unlock()
		if ok && time.Now().Before(e.exp) {
			return e.raw, nil
		}
		return call.raw, call.err
	}

	call := &receiptCall{done: make(chan struct{})}
	r.inflight[cacheKey] = call
	r.mu.Unlock()

	raw, err := r.Source.Fetch(ctx, key, payload)

	r.mu.Lock()
	if err == nil {
		// Cache the fetch result unless a fresh solved receipt was seeded
		// while the request was in flight (that entry has the longer TTL).
		if e, ok := r.receipts[cacheKey]; !ok || !time.Now().Before(e.exp) {
			r.receipts[cacheKey] = receiptEntry{raw: raw, exp: time.Now().Add(r.rawTTL)}
		}
	}
	call.raw = raw
	call.err = err
	delete(r.inflight, cacheKey)
	close(call.done)
	r.mu.Unlock()

	return raw, err
}

func (r *Resolver) sweepLocked() {
	now := time.Now()
	for k, e := range r.receipts {
		if now.After(e.exp) {
			delete(r.receipts, k)
		}
	}
}

// MultiResolver is the per-UF dispatch table. It routes a request to the UF's
// own Resolver by the access key's first two digits (the IBGE code) and
// answers ErrUnsupportedUF for states with no fixture-verified Parser.
type MultiResolver struct {
	byUF map[string]*Resolver
}

// NewMultiResolver builds a dispatch table from "IBGE code" → resolver pairs.
func NewMultiResolver(byUF map[string]*Resolver) *MultiResolver {
	return &MultiResolver{byUF: byUF}
}

// Resolve dispatches by UF and delegates to that state's resolver. A resolver
// is created only for a UF whose portal is probe-verified, so any other state
// (including states whose portal is JavaScript/captcha-gated, or has no
// public sample receipt yet) fails with ErrUnsupportedUF.
func (m *MultiResolver) Resolve(ctx context.Context, key AccessKey, payload string) (*domain.Nfce, error) {
	r, ok := m.byUF[key.UFCode()]

	if !ok {
		return nil, fmt.Errorf("%w: %s", ErrUnsupportedUF, ufDisplayName(key.UFCode()))
	}

	return r.Resolve(ctx, key, payload)
}

// CacheSolved delegates the captcha bridge to the UF's own resolver. An
// unsupported UF is a no-op: there is no cached receipt to serve because the
// resolve path would have rejected the key before any fetch.
func (m *MultiResolver) CacheSolved(key AccessKey, raw []byte) {
	if r, ok := m.byUF[key.UFCode()]; ok {
		r.CacheSolved(key, raw)
	}
}

func ufDisplayName(uf string) string {
	if cfg, ok := LookupUF(uf); ok {
		return fmt.Sprintf("%s (%s)", cfg.FullName, cfg.UF)
	}
	return fmt.Sprintf("UF %s", uf)
}

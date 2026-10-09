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
// fetch → parse flow. It also holds a short-lived "solved receipt" cache: when
// a captcha challenge is solved (see ChallengeStore + CaptchaSolver), the raw
// DANFE is seeded here keyed by access key, so the follow-up fetch that /
// nfce/submit performs does not trip the challenge again.
type Resolver struct {
	Source Source
	Parser Parser

	mu     sync.Mutex
	solved map[string]solvedEntry
	ttl    time.Duration // solved-receipt TTL (overrides the default)
}

// solvedEntry is a cached DANFE and when it expires.
type solvedEntry struct {
	raw []byte
	exp time.Time
}

// solvedReceiptTTL keeps a solved receipt alive across the draft → commit
// window (the pending preview itself lives PreviewTTL).
const solvedReceiptTTL = 20 * time.Minute

// NewResolver creates a resolver from a source and parser.
func NewResolver(src Source, p Parser) *Resolver {
	return &Resolver{Source: src, Parser: p, solved: make(map[string]solvedEntry), ttl: solvedReceiptTTL}
}

// CacheSolved stores a raw DANFE bytes for key so the next Resolve for that
// key serves the receipt without another portal fetch (the captcha bridge).
func (r *Resolver) CacheSolved(key AccessKey, raw []byte) {
	if raw == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.sweepLocked()
	r.solved[key.String()] = solvedEntry{raw: raw, exp: time.Now().Add(r.ttl)}
}

// Resolve fetches the raw DANFE from the source and parses it with the
// configured parser. A cached solved receipt (captcha path) short-circuits the
// fetch entirely — the parser still runs, so the returned document is fresh.
// Any error returned is already domain-typed
// (ErrInvalidKey, CaptchaError, ErrUnsupportedUF, etc.).
func (r *Resolver) Resolve(ctx context.Context, key AccessKey, payload string) (*domain.Nfce, error) {
	raw, err := r.raw(ctx, key, payload)
	if err != nil {
		return nil, err
	}
	return r.Parser.Parse(ctx, key, raw)
}

func (r *Resolver) raw(ctx context.Context, key AccessKey, payload string) ([]byte, error) {
	r.mu.Lock()

	if e, ok := r.solved[key.String()]; ok && !e.exp.IsZero() && time.Now().Before(e.exp) {
		raw := e.raw
		r.mu.Unlock()
		return raw, nil
	}

	r.mu.Unlock()

	raw, err := r.Source.Fetch(ctx, key, payload)
	if err != nil {
		return nil, err
	}
	return raw, nil
}

func (r *Resolver) sweepLocked() {
	now := time.Now()
	for k, e := range r.solved {
		if now.After(e.exp) {
			delete(r.solved, k)
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

func ufDisplayName(uf string) string {
	if cfg, ok := LookupUF(uf); ok {
		return fmt.Sprintf("%s (%s)", cfg.FullName, cfg.UF)
	}
	return fmt.Sprintf("UF %s", uf)
}

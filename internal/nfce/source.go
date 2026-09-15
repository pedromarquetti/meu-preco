package nfce

import (
	"context"
	"fmt"

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
// fetch → parse flow.
type Resolver struct {
	Source Source
	Parser Parser
}

// NewResolver creates a resolver from a source and parser.
func NewResolver(src Source, p Parser) *Resolver {
	return &Resolver{Source: src, Parser: p}
}

// Resolve fetches the raw DANFE from the source and parses it with the
// configured parser. Any error returned is already domain-typed
// (ErrInvalidKey, CaptchaError, ErrUnsupportedUF, etc.).
func (r *Resolver) Resolve(ctx context.Context, key AccessKey, payload string) (*domain.Nfce, error) {
	raw, err := r.Source.Fetch(ctx, key, payload)

	if err != nil {
		return nil, err
	}

	return r.Parser.Parse(ctx, key, raw)
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

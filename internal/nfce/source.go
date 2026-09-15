package nfce

import (
	"context"

	"meupreco/internal/domain"
)

// Source is the abstraction over data providers (SEFAZ-SP portal, other UFs,
// mock data). Implementations return raw page bytes for a given access key.
type Source interface {
	Fetch(ctx context.Context, key AccessKey) ([]byte, error)
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
// (ErrInvalidKey, CaptchaError, etc.).
func (r *Resolver) Resolve(ctx context.Context, key AccessKey) (*domain.Nfce, error) {
	raw, err := r.Source.Fetch(ctx, key)
	if err != nil {
		return nil, err
	}
	return r.Parser.Parse(ctx, key, raw)
}

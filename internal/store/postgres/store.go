// Package store implements the domain repository interfaces on top of
// PostgreSQL using pgx. It is the only place that knows about SQL.
package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"meupreco/internal/domain"
)

// Store implements domain.Stores on a pgx pool.
type Store struct {
	pool *pgxpool.Pool
}

// Options controls Store construction.
type Options struct {
	// DSN is the postgres:// connection string (built from config's
	// POSTGRES_* / DATABASE_NAME env vars).
	DSN string
	// MaxConnLifetime allows callers to cap connection reuse (default 30m).
	MaxConnLifetime time.Duration
}

// New opens the pool (no network round-trip yet; Ping verifies connectivity).
func New(opts Options) (*Store, error) {
	if opts.MaxConnLifetime == 0 {
		opts.MaxConnLifetime = 30 * time.Minute
	}

	cfg, err := pgxpool.ParseConfig(opts.DSN)
	if err != nil {
		return nil, fmt.Errorf("store: parse DSN: %w", err)
	}
	cfg.MaxConnLifetime = opts.MaxConnLifetime

	pool, err := pgxpool.NewWithConfig(context.Background(), cfg)
	if err != nil {
		return nil, fmt.Errorf("store: create pool: %w", err)
	}

	return &Store{pool: pool}, nil
}

// NewFromPool wraps an existing pool (used by tests).
func NewFromPool(pool *pgxpool.Pool) *Store {
	return &Store{pool: pool}
}

// Pool exposes the underlying pool (needed by test helpers).
func (s *Store) Pool() *pgxpool.Pool { return s.pool }

// Ping verifies the connection is alive. A nil Store (or one whose pool was
// never opened) degrades to an error so callers can report 503 instead of
// panicking on a nil pool dereference.
func (s *Store) Ping(ctx context.Context) error {
	if s == nil || s.pool == nil {
		return errors.New("store: database pool not initialized")
	}
	return s.pool.Ping(ctx)
}

// Close releases all pooled connections.
func (s *Store) Close() {
	if s == nil || s.pool == nil {
		return
	}
	s.pool.Close()
}

// Users returns the user repository.
func (s *Store) Users() domain.UserStore { return userRepo{s} }

// Supermarkets returns the supermarket repository.
func (s *Store) Supermarkets() domain.SupermarketStore { return supermarketRepo{s} }

// Products returns the product repository.
func (s *Store) Products() domain.ProductStore { return productRepo{s} }

// Nfces returns the receipt repository.
func (s *Store) Nfces() domain.NfceStore { return nfceRepo{s} }

// PriceLogs returns the price-log repository.
func (s *Store) PriceLogs() domain.PriceLogStore { return priceLogRepo{s} }

// Previews returns the preview repository.
func (s *Store) Previews() domain.PreviewStore { return previewRepo{s} }

// CommoditySynonyms returns the commodity synonym repository.
func (s *Store) CommoditySynonyms() domain.CommoditySynonymStore { return synonymRepo{s} }

// RefreshTokens returns the refresh-token repository.
func (s *Store) RefreshTokens() domain.RefreshTokenStore { return refreshTokenRepo{s} }

// translateError converts a pgx error into the ported sentinels.
func translateError(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, pgx.ErrNoRows):
		return domain.ErrNotFound
	default:
		return err
	}
}

// isUniqueViolation reports whether err is a PostgreSQL unique_violation.
func isUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505"
}

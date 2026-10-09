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

// queryer is the minimal SQL surface shared by pgxpool.Pool and pgx.Tx so a
// repository can bind to either the pool (steady-state calls) or a caller-
// owned transaction (the Phase 5 commit path) without knowing which.
type queryer interface {
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
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
func (s *Store) Users() domain.UserStore { return userRepo{db: s.pool} }

// Supermarkets returns the supermarket repository.
func (s *Store) Supermarkets() domain.SupermarketStore { return supermarketRepo{db: s.pool} }

// Products returns the product repository.
func (s *Store) Products() domain.ProductStore { return productRepo{db: s.pool} }

// Nfces returns the receipt repository.
func (s *Store) Nfces() domain.NfceStore { return nfceRepo{db: s.pool} }

// PriceLogs returns the price-log repository.
func (s *Store) PriceLogs() domain.PriceLogStore { return priceLogRepo{db: s.pool} }

// Previews returns the preview repository.
func (s *Store) Previews() domain.PreviewStore { return previewRepo{db: s.pool} }

// CommoditySynonyms returns the commodity synonym repository.
func (s *Store) CommoditySynonyms() domain.CommoditySynonymStore { return synonymRepo{db: s.pool} }

// RefreshTokens returns the refresh-token repository.
func (s *Store) RefreshTokens() domain.RefreshTokenStore { return refreshTokenRepo{db: s.pool} }

// Tx runs fn inside a real PostgreSQL transaction. The store handed to fn
// binds its repositories to the open pgx transaction, so every write it makes
// commits or rolls back together; fn returning an error aborts the whole unit.
func (s *Store) Tx(ctx context.Context, fn func(domain.Stores) error) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}

	if err := fn(&txStores{db: tx}); err != nil {
		_ = tx.Rollback(ctx)
		return err
	}

	return tx.Commit(ctx)
}

// txStores is the transaction-bound view of Store: identical repository
// surface, every query routed through one pgx.Tx.
type txStores struct{ db queryer }

func (s *txStores) Users() domain.UserStore               { return userRepo{db: s.db} }
func (s *txStores) Supermarkets() domain.SupermarketStore { return supermarketRepo{db: s.db} }
func (s *txStores) Products() domain.ProductStore         { return productRepo{db: s.db} }
func (s *txStores) Nfces() domain.NfceStore               { return nfceRepo{db: s.db} }
func (s *txStores) PriceLogs() domain.PriceLogStore       { return priceLogRepo{db: s.db} }
func (s *txStores) Previews() domain.PreviewStore         { return previewRepo{db: s.db} }
func (s *txStores) CommoditySynonyms() domain.CommoditySynonymStore {
	return synonymRepo{db: s.db}
}
func (s *txStores) RefreshTokens() domain.RefreshTokenStore { return refreshTokenRepo{db: s.db} }
func (s *txStores) Ping(context.Context) error              { return nil }
func (s *txStores) Close()                                  {} // no-op: owned by Tx

// Tx on a transaction-bound store is a nested-transaction attempt; the outer
// transaction is already the single atomic unit, so it is rejected.
func (s *txStores) Tx(context.Context, func(domain.Stores) error) error { return errNestedTx }

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

// Package store owns Postgres access: the connection pool, migrations
// (goose for Exemplar's tables, River's own for the job queue), the
// sqlc-generated queries in store/queries, and River job arguments.
package store

import (
	"context"
	"fmt"
	"io/fs"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"
	"github.com/riverqueue/river/riverdriver/riverpgxv5"
	"github.com/riverqueue/river/rivermigrate"

	"github.com/koblas/exemplar/db"
	"github.com/koblas/exemplar/internal/store/queries"
)

// Store is a pool plus typed queries.
type Store struct {
	Pool *pgxpool.Pool
	*queries.Queries
}

// Open connects to Postgres and verifies the connection.
func Open(ctx context.Context, url string) (*Store, error) { return OpenWithConns(ctx, url, 0) }

// OpenWithConns is Open with a pool of at least minConns connections; a
// larger pool_max_conns in url is kept.
func OpenWithConns(ctx context.Context, url string, minConns int32) (*Store, error) {
	cfg, err := pgxpool.ParseConfig(url)
	if err != nil {
		return nil, fmt.Errorf("parse database url: %w", err)
	}
	cfg.MaxConns = max(cfg.MaxConns, minConns)
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("connect: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("ping database: %w", err)
	}
	return &Store{Pool: pool, Queries: queries.New(pool)}, nil
}

// Close releases the pool.
func (s *Store) Close() { s.Pool.Close() }

// Migrate applies Exemplar's goose migrations and River's migrations.
func (s *Store) Migrate(ctx context.Context) error {
	migrations, err := fs.Sub(db.Migrations, "migrations")
	if err != nil {
		return err
	}
	sqlDB := stdlib.OpenDBFromPool(s.Pool)
	defer func() { _ = sqlDB.Close() }()
	provider, err := goose.NewProvider(goose.DialectPostgres, sqlDB, migrations)
	if err != nil {
		return fmt.Errorf("goose: %w", err)
	}
	if _, err := provider.Up(ctx); err != nil {
		return fmt.Errorf("apply migrations: %w", err)
	}
	migrator, err := rivermigrate.New(riverpgxv5.New(s.Pool), nil)
	if err != nil {
		return fmt.Errorf("river migrator: %w", err)
	}
	if _, err := migrator.Migrate(ctx, rivermigrate.DirectionUp, nil); err != nil {
		return fmt.Errorf("apply river migrations: %w", err)
	}
	return nil
}

// InTx runs fn in a transaction, committing only if fn succeeds. Business
// writes and the next job's insert share the transaction.
func (s *Store) InTx(ctx context.Context, fn func(pgx.Tx, *queries.Queries) error) error {
	return pgx.BeginFunc(ctx, s.Pool, func(tx pgx.Tx) error {
		return fn(tx, s.WithTx(tx))
	})
}

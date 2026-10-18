// Package store owns Postgres access: the connection pool, migrations
// (goose for Exemplar's tables, River's own for the job queue), the
// sqlc-generated queries in store/queries, and River job arguments.
package store

import (
	"context"
	"fmt"
	"io/fs"
	"time"

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
	*queries.Queries

	Pool *pgxpool.Pool
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

// migrateLockKey is the session-level advisory lock that serializes
// Migrate across pods ("exemplar" in ASCII). Neither goose's provider nor
// River's migrator locks on its own.
const migrateLockKey int64 = 0x6578656d706c6172

// Migrate applies Exemplar's goose migrations and River's migrations. The
// webhook, api, worker and learn pods all migrate at startup, so it holds
// an advisory lock on a dedicated connection while it runs; others wait,
// then find nothing left to apply.
func (s *Store) Migrate(ctx context.Context) error {
	// Its own connection, outside the pool the migrations use, so a small
	// pool cannot deadlock against the lock holder.
	conn, err := pgx.ConnectConfig(ctx, s.Pool.Config().ConnConfig.Copy())
	if err != nil {
		return fmt.Errorf("connect for migration lock: %w", err)
	}
	// Closing the session releases the lock.
	defer func() {
		cctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
		defer cancel()
		_ = conn.Close(cctx)
	}()
	if err := tryLock(ctx, conn); err != nil {
		return err
	}
	return s.migrate(ctx)
}

// tryLock polls pg_try_advisory_lock rather than blocking in
// pg_advisory_lock: a session blocked in that statement holds a snapshot,
// and CREATE INDEX CONCURRENTLY in the lock holder's migrations waits for
// every such snapshot, so the two would deadlock.
func tryLock(ctx context.Context, conn *pgx.Conn) error {
	t := time.NewTicker(500 * time.Millisecond)
	defer t.Stop()
	for {
		var ok bool
		if err := conn.QueryRow(ctx, "SELECT pg_try_advisory_lock($1)", migrateLockKey).Scan(&ok); err != nil {
			return fmt.Errorf("take migration lock: %w", err)
		}
		if ok {
			return nil
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("wait for migration lock: %w", ctx.Err())
		case <-t.C:
		}
	}
}

func (s *Store) migrate(ctx context.Context) error {
	migrations, err := fs.Sub(db.Migrations, "migrations")
	if err != nil {
		return fmt.Errorf("migrations dir: %w", err)
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
	if err := pgx.BeginFunc(ctx, s.Pool, func(tx pgx.Tx) error {
		return fn(tx, s.WithTx(tx))
	}); err != nil {
		return fmt.Errorf("store: transaction: %w", err)
	}
	return nil
}

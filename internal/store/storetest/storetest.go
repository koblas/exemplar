// Package storetest runs Postgres for tests in a container (testcontainers-go)
// and gives each test its own migrated database.
//
// A test package calls Main from TestMain; that starts one pgvector Postgres
// container for the package and removes it afterwards. New then creates a
// fresh database per test. Tests skip when no container runtime is
// available, unless EXEMPLAR_REQUIRE_DB=1 (set in CI), which makes them fail.
package storetest

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net/url"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/modules/postgres"

	"github.com/koblas/exemplar/internal/store"
)

// Image is the Postgres image the tests run: Postgres 16 with pgvector,
// matching the production requirement.
const Image = "pgvector/pgvector:0.8.0-pg16"

var (
	adminURL   string // set by Main when the container is up
	skipReason string // set by Main when it is not
)

// Main starts the package's Postgres container, runs the tests, and removes
// the container. Use it from TestMain:
//
//	func TestMain(m *testing.M) { storetest.Main(m) }
func Main(m *testing.M) {
	os.Exit(run(m))
}

func run(m *testing.M) int {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	ctr, err := postgres.Run(ctx, Image,
		postgres.WithDatabase("postgres"),
		postgres.WithUsername("postgres"),
		postgres.WithPassword("postgres"),
		postgres.BasicWaitStrategies(),
	)
	if err == nil {
		adminURL, err = ctr.ConnectionString(ctx, "sslmode=disable")
	}
	cancel()
	if err != nil {
		skipReason = fmt.Sprintf("Postgres test container unavailable (is a container runtime running?): %v", err)
		if os.Getenv("EXEMPLAR_REQUIRE_DB") == "1" {
			fmt.Fprintln(os.Stderr, "storetest:", skipReason)
			if ctr != nil {
				_ = testcontainers.TerminateContainer(ctr)
			}
			return 1
		}
	}
	code := m.Run()
	if ctr != nil {
		if err := testcontainers.TerminateContainer(ctr); err != nil {
			fmt.Fprintln(os.Stderr, "storetest: terminate container:", err)
		}
	}
	return code
}

// New creates a fresh database in the package's container, migrates it, and
// drops it when the test ends.
func New(tb testing.TB) *store.Store {
	tb.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	s, err := store.Open(ctx, EmptyURL(tb))
	if err != nil {
		tb.Fatalf("open test database: %v", err)
	}
	tb.Cleanup(s.Close)
	if err := s.Migrate(ctx); err != nil {
		tb.Fatalf("migrate: %v", err)
	}
	return s
}

// EmptyURL creates a fresh, unmigrated database in the package's container
// and returns its URL; the database is dropped when the test ends, after
// cleanups registered later (such as closing a store on it).
func EmptyURL(tb testing.TB) string {
	tb.Helper()
	if adminURL == "" {
		if skipReason == "" {
			tb.Fatal("storetest used without storetest.Main in TestMain")
		}
		tb.Skip(skipReason)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()

	var b [6]byte
	_, _ = rand.Read(b[:])
	name := "exemplar_test_" + hex.EncodeToString(b[:])

	conn, err := pgx.Connect(ctx, adminURL)
	if err != nil {
		tb.Fatalf("connect admin: %v", err)
	}
	defer func() { _ = conn.Close(ctx) }()
	if _, err := conn.Exec(ctx, "CREATE DATABASE "+name); err != nil {
		tb.Fatalf("create database: %v", err)
	}
	tb.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		c, err := pgx.Connect(ctx, adminURL)
		if err != nil {
			tb.Logf("drop %s: %v", name, err)
			return
		}
		defer func() { _ = c.Close(ctx) }()
		if _, err := c.Exec(ctx, "DROP DATABASE IF EXISTS "+name+" WITH (FORCE)"); err != nil {
			tb.Logf("drop %s: %v", name, err)
		}
	})

	u, err := url.Parse(adminURL)
	if err != nil {
		tb.Fatalf("parse url: %v", err)
	}
	u.Path = "/" + name
	return u.String()
}

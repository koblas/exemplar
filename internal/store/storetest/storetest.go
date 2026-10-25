// Package storetest runs Postgres for tests in a container (testcontainers-go)
// and gives each test its own migrated database.
//
// A test package calls Main from TestMain; that starts one pgvector Postgres
// container for the package, migrates a template database in it, and removes
// the container afterwards. New then clones the template per test. Tests
// skip when no container runtime is available, unless EXEMPLAR_REQUIRE_DB=1
// (set in CI), which makes them fail.
//
// The container starts in TestMain rather than on first use because many
// tests call New inside a synctest bubble: starting it there would spend the
// bubble's fake time on the wait strategy and bind the net package's first
// DNS lookup to that bubble.
package storetest

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net/url"
	"os"
	"strconv"
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

// templateDB is the migrated database New clones.
const templateDB = "exemplar_template"

var (
	adminURL   string // set by Main when the container is up
	skipReason string // set by Main when it is not
)

// Main starts the package's Postgres container, runs the tests, and removes
// the container. Use it from TestMain:
//
//	func TestMain(m *testing.M) { storetest.Main(m) }
func Main(m *testing.M) {
	os.Exit(Run(m))
}

// Run is Main without the exit, for a TestMain that wraps it:
//
//	func TestMain(m *testing.M) { os.Exit(synth.RunCached(func() int { return storetest.Run(m) })) }
func Run(m *testing.M) int {
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
	if err == nil {
		err = migrateTemplate(ctx, adminURL)
	}
	cancel()
	if err != nil {
		adminURL = ""
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

// migrateTemplate creates and migrates the database New clones. Its
// connections are closed when it returns, as CREATE DATABASE ... TEMPLATE
// requires.
func migrateTemplate(ctx context.Context, admin string) error {
	if err := exec(ctx, admin, "CREATE DATABASE "+templateDB); err != nil {
		return err
	}
	s, err := store.Open(ctx, withDB(admin, templateDB))
	if err != nil {
		return fmt.Errorf("open template database: %w", err)
	}
	defer s.Close()
	if err := s.Migrate(ctx); err != nil {
		return fmt.Errorf("migrate template database: %w", err)
	}
	return nil
}

// Conns is a test store's pool size: what pgxpool's default of max(4,
// NumCPU) gives on CI's 4-CPU runners, pinned so a test that holds
// connections behaves the same on a larger machine as on CI.
const Conns = 4

// New clones the migrated template into a fresh database in the package's
// container and drops it when the test ends. Its pool has Conns connections.
func New(tb testing.TB) *store.Store {
	tb.Helper()
	return NewWithConns(tb, 0)
}

// NewWithConns is New with a pool of max(Conns, minConns) connections,
// for a test that holds more (advisory-lock slots) than Conns allows.
func NewWithConns(tb testing.TB, minConns int32) *store.Store {
	tb.Helper()
	u, err := url.Parse(newDatabase(tb, templateDB))
	if err != nil {
		tb.Fatalf("parse url: %v", err)
	}
	q := u.Query()
	q.Set("pool_max_conns", strconv.Itoa(Conns))
	u.RawQuery = q.Encode()
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	s, err := store.OpenWithConns(ctx, u.String(), minConns)
	if err != nil {
		tb.Fatalf("open test database: %v", err)
	}
	tb.Cleanup(s.Close)
	return s
}

// EmptyURL creates a fresh, unmigrated database in the package's container
// and returns its URL; the database is dropped when the test ends, after
// cleanups registered later (such as closing a store on it).
func EmptyURL(tb testing.TB) string {
	tb.Helper()
	return newDatabase(tb, "")
}

// newDatabase creates a database, cloned from template when one is named,
// and drops it when the test ends.
func newDatabase(tb testing.TB, template string) string {
	tb.Helper()
	if adminURL == "" {
		if skipReason == "" {
			tb.Fatal("storetest used without storetest.Main in TestMain")
		}
		tb.Skip(skipReason)
	}
	var b [6]byte
	_, _ = rand.Read(b[:])
	name := "exemplar_test_" + hex.EncodeToString(b[:])
	stmt := "CREATE DATABASE " + name
	if template != "" {
		stmt += " TEMPLATE " + template
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	if err := exec(ctx, adminURL, stmt); err != nil {
		tb.Fatalf("create database: %v", err)
	}
	tb.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if err := exec(ctx, adminURL, "DROP DATABASE IF EXISTS "+name+" WITH (FORCE)"); err != nil {
			tb.Logf("drop %s: %v", name, err)
		}
	})
	return withDB(adminURL, name)
}

// exec runs one statement on its own admin connection.
func exec(ctx context.Context, admin, stmt string) error {
	conn, err := pgx.Connect(ctx, admin)
	if err != nil {
		return fmt.Errorf("connect admin: %w", err)
	}
	defer func() { _ = conn.Close(ctx) }()
	if _, err := conn.Exec(ctx, stmt); err != nil {
		return fmt.Errorf("%s: %w", stmt, err)
	}
	return nil
}

// withDB is the admin URL with its database replaced by name. The admin
// URL is testcontainers' connection string, which always parses.
func withDB(admin, name string) string {
	u, _ := url.Parse(admin)
	u.Path = "/" + name
	return u.String()
}

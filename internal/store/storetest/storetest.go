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
func New(t testing.TB) *store.Store {
	t.Helper()
	if adminURL == "" {
		if skipReason == "" {
			t.Fatal("storetest.New called without storetest.Main in TestMain")
		}
		t.Skip(skipReason)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()

	var b [6]byte
	_, _ = rand.Read(b[:])
	name := "exemplar_test_" + hex.EncodeToString(b[:])

	conn, err := pgx.Connect(ctx, adminURL)
	if err != nil {
		t.Fatalf("connect admin: %v", err)
	}
	defer func() { _ = conn.Close(ctx) }()
	if _, err := conn.Exec(ctx, "CREATE DATABASE "+name); err != nil {
		t.Fatalf("create database: %v", err)
	}

	u, err := url.Parse(adminURL)
	if err != nil {
		t.Fatalf("parse url: %v", err)
	}
	u.Path = "/" + name
	s, err := store.Open(ctx, u.String())
	if err != nil {
		t.Fatalf("open test database: %v", err)
	}
	if err := s.Migrate(ctx); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	t.Cleanup(func() {
		s.Close()
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		c, err := pgx.Connect(ctx, adminURL)
		if err != nil {
			t.Logf("drop %s: %v", name, err)
			return
		}
		defer func() { _ = c.Close(ctx) }()
		if _, err := c.Exec(ctx, "DROP DATABASE IF EXISTS "+name+" WITH (FORCE)"); err != nil {
			t.Logf("drop %s: %v", name, err)
		}
	})
	return s
}

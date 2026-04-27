//go:build integration

// Package setup contains helpers for integration tests that spin up real
// dependencies (databases, mock HTTP servers) using testcontainers-go.
//
// These tests are gated behind the `integration` build tag so that
// `go test ./...` in a developer's normal workflow stays fast and offline.
// Run them with:
//
//   go test -tags=integration ./testing/integration/...
//
// CI runs the suite as a separate job that has Docker available.
package setup

import (
	"context"
	"database/sql"
	"fmt"
	"testing"
	"time"

	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"
)

// Postgres returns a *sql.DB connected to a freshly started container with
// the provided init SQL applied. The container is torn down via t.Cleanup,
// so each test gets isolation without manual teardown.
//
// We deliberately START a fresh container per test PACKAGE (not per test) by
// using a sync.Once at the call site if needed; per-test containers would be
// ~5s overhead each.
func Postgres(t *testing.T, initSQL string) *sql.DB {
	t.Helper()
	ctx := context.Background()

	pgC, err := postgres.Run(ctx,
		"postgres:16-alpine",
		postgres.WithDatabase("orders"),
		postgres.WithUsername("test"),
		postgres.WithPassword("test"),
		testcontainers.WithWaitStrategy(
			wait.ForLog("database system is ready to accept connections").
				WithOccurrence(2).
				WithStartupTimeout(60*time.Second),
		),
	)
	if err != nil {
		t.Fatalf("start postgres: %v", err)
	}
	t.Cleanup(func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = pgC.Terminate(shutdownCtx)
	})

	dsn, err := pgC.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		t.Fatalf("connection string: %v", err)
	}

	db, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	if err := db.PingContext(ctx); err != nil {
		t.Fatalf("ping: %v", err)
	}

	if initSQL != "" {
		if _, err := db.ExecContext(ctx, initSQL); err != nil {
			t.Fatalf("init sql: %v\n%s", err, initSQL)
		}
	}
	return db
}

// WireMockEndpoint starts a wiremock container and returns its base URL.
// Stubs are configured via the returned *WireMockClient; see payment_client_test.go.
func WireMockEndpoint(t *testing.T) string {
	t.Helper()
	ctx := context.Background()

	req := testcontainers.ContainerRequest{
		Image:        "wiremock/wiremock:3.9.1",
		ExposedPorts: []string{"8080/tcp"},
		WaitingFor:   wait.ForHTTP("/__admin/health").WithPort("8080/tcp").WithStartupTimeout(30 * time.Second),
	}
	wm, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
		ContainerRequest: req,
		Started:          true,
	})
	if err != nil {
		t.Fatalf("start wiremock: %v", err)
	}
	t.Cleanup(func() { _ = wm.Terminate(ctx) })

	host, _ := wm.Host(ctx)
	port, _ := wm.MappedPort(ctx, "8080/tcp")
	return fmt.Sprintf("http://%s:%s", host, port.Port())
}
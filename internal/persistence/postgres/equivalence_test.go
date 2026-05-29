package postgres

import (
	"context"
	"testing"
	"time"

	"github.com/chepherd/chepherd/internal/persistence/equivalence"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"
)

// TestEquivalence_Postgres runs the chepherd v0.9.2 persistence
// backend-equivalence suite against PostgreSQL via testcontainers-go.
// The same suite (internal/persistence/equivalence/equivalence.go)
// runs against SQLite from sibling test. Behavioral drift between
// the two backends surfaces as a test failure on the non-conforming
// side.
//
// Skipped in -short mode and when Docker isn't available — CI runners
// with Docker socket access run it for real; local dev without Docker
// gets the SQLite suite as the everyday gate.
//
// Refs #208.
func TestEquivalence_Postgres(t *testing.T) {
	if testing.Short() {
		t.Skip("postgres equivalence requires Docker; skipping in -short mode")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	container, err := tcpostgres.Run(ctx,
		"postgres:16-alpine",
		tcpostgres.WithDatabase("chepherd_test"),
		tcpostgres.WithUsername("test"),
		tcpostgres.WithPassword("test"),
		tcpostgres.BasicWaitStrategies(),
	)
	if err != nil {
		t.Skipf("postgres container unavailable (Docker not running?): %v", err)
	}
	t.Cleanup(func() {
		if err := container.Terminate(ctx); err != nil {
			t.Logf("terminate container: %v", err)
		}
	})

	dsn, err := container.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		t.Fatalf("ConnectionString: %v", err)
	}

	store, err := NewStore(ctx, dsn)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	equivalence.RunAll(t, store)
}

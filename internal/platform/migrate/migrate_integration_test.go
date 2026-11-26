//go:build integration

package migrate_test

import (
	"context"
	"testing"

	walletmigrations "github.com/donaina/echo/db/wallet"
	"github.com/donaina/echo/internal/platform/migrate"
	"github.com/donaina/echo/internal/platform/postgres"
	"github.com/donaina/echo/internal/platform/testkit"
)

// TestApplyFromEmptyAndRefuseToServeBehind is Stage 0's migration proof: the
// tool applies to a real, empty PostgreSQL, is idempotent, and a service
// cannot start against a database that is behind the embedded migrations.
func TestApplyFromEmptyAndRefuseToServeBehind(t *testing.T) {
	ctx := context.Background()
	pool, err := postgres.NewPool(ctx, testkit.Postgres(t), 2)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer pool.Close()

	want, err := migrate.Latest(walletmigrations.Migrations)
	if err != nil {
		t.Fatalf("latest: %v", err)
	}
	if want < 1 {
		t.Fatalf("latest = %d, want at least the baseline migration", want)
	}

	if got, err := migrate.Version(ctx, pool); err != nil || got != 0 {
		t.Fatalf("version on an empty database = %d, %v; want 0", got, err)
	}

	if err := migrate.EnsureCurrent(ctx, pool, walletmigrations.Migrations); err == nil {
		t.Fatal("EnsureCurrent passed on an unmigrated database")
	}

	if err := migrate.Up(ctx, pool, walletmigrations.Migrations); err != nil {
		t.Fatalf("Up: %v", err)
	}
	if got, err := migrate.Version(ctx, pool); err != nil || got != want {
		t.Fatalf("version after Up = %d, %v; want %d", got, err, want)
	}
	if err := migrate.EnsureCurrent(ctx, pool, walletmigrations.Migrations); err != nil {
		t.Fatalf("EnsureCurrent after Up: %v", err)
	}

	// Applying twice is a no-op, not an error.
	if err := migrate.Up(ctx, pool, walletmigrations.Migrations); err != nil {
		t.Fatalf("second Up: %v", err)
	}
}

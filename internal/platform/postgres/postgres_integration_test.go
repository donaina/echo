//go:build integration

package postgres_test

import (
	"context"
	"testing"
	"time"

	"github.com/donaina/echo/internal/platform/postgres"
	"github.com/donaina/echo/internal/platform/testkit"
)

// TestReadinessFollowsTheDatabase proves that /readyz's answer is the
// database's answer: true while it is reachable, false once it stops.
func TestReadinessFollowsTheDatabase(t *testing.T) {
	ctx := context.Background()
	container, dsn := testkit.PostgresContainer(t)

	pool, err := postgres.NewPool(ctx, dsn, 2)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer pool.Close()

	ready := postgres.Ready(pool)
	if err := ready(ctx); err != nil {
		t.Fatalf("ready against a live database: %v", err)
	}

	if err := container.Stop(ctx, nil); err != nil {
		t.Fatalf("stop postgres: %v", err)
	}

	deadline := time.Now().Add(15 * time.Second)
	for ready(ctx) == nil {
		if time.Now().After(deadline) {
			t.Fatal("readiness still passed after the database stopped")
		}
		time.Sleep(200 * time.Millisecond)
	}
}

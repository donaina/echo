//go:build integration

package testkit

import (
	"context"
	"testing"
	"time"

	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"
)

// Postgres starts a real PostgreSQL 17 and returns its DSN. The container is
// terminated when the test ends. A test that needs the database's constraints
// to be real uses this, never a mock.
func Postgres(t *testing.T) string {
	t.Helper()
	_, dsn := PostgresContainer(t)
	return dsn
}

// PostgresContainer is Postgres plus the container, for a test that needs to
// stop the database and watch what breaks.
func PostgresContainer(t *testing.T) (*postgres.PostgresContainer, string) {
	t.Helper()
	ctx := context.Background()
	c, err := postgres.Run(ctx, "postgres:17-alpine",
		postgres.WithDatabase("echo"),
		postgres.WithUsername("echo"),
		postgres.WithPassword("echo"),
		postgres.BasicWaitStrategies(),
	)
	if err != nil {
		t.Fatalf("start postgres: %v", err)
	}
	t.Cleanup(func() { _ = c.Terminate(context.Background()) })
	dsn, err := c.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		t.Fatalf("postgres connection string: %v", err)
	}
	return c, dsn
}

// NATS starts a JetStream-enabled NATS and returns its URL.
func NATS(t *testing.T) string {
	t.Helper()
	return start(t, "nats:2.11-alpine", "4222/tcp", []string{"-js"}, "nats://")
}

// Redis starts Redis and returns its URL.
func Redis(t *testing.T) string {
	t.Helper()
	return start(t, "redis:7-alpine", "6379/tcp", nil, "redis://")
}

func start(t *testing.T, image, port string, cmd []string, scheme string) string {
	t.Helper()
	ctx := context.Background()
	c, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
		ContainerRequest: testcontainers.ContainerRequest{
			Image:        image,
			ExposedPorts: []string{port},
			Cmd:          cmd,
			WaitingFor:   wait.ForListeningPort(port).WithStartupTimeout(60 * time.Second),
		},
		Started: true,
	})
	if err != nil {
		t.Fatalf("start %s: %v", image, err)
	}
	t.Cleanup(func() { _ = c.Terminate(context.Background()) })

	host, err := c.Host(ctx)
	if err != nil {
		t.Fatalf("%s host: %v", image, err)
	}
	mapped, err := c.MappedPort(ctx, port)
	if err != nil {
		t.Fatalf("%s port: %v", image, err)
	}
	return scheme + host + ":" + mapped.Port()
}

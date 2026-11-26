// Package migrate applies a service's embedded migrations and refuses to let a
// service serve against a database that is behind them. The migrations are
// embedded in the binary, so the schema a release expects is the schema it
// ships with.
package migrate

import (
	"context"
	"fmt"
	"io/fs"
	"regexp"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"
)

// Dir is the directory inside each service's embedded filesystem.
const Dir = "migrations"

// Up applies every pending migration. It is safe to run against a database
// that is already current.
func Up(ctx context.Context, pool *pgxpool.Pool, migrations fs.FS) error {
	db := stdlib.OpenDBFromPool(pool)
	defer db.Close()

	goose.SetBaseFS(migrations)
	if err := goose.SetDialect("postgres"); err != nil {
		return fmt.Errorf("migrate: set dialect: %w", err)
	}
	if err := goose.UpContext(ctx, db, Dir); err != nil {
		return fmt.Errorf("migrate up: %w", err)
	}
	return nil
}

// Version reports the migration version the database is at, or 0 if it has
// never been migrated.
func Version(ctx context.Context, pool *pgxpool.Pool) (int64, error) {
	db := stdlib.OpenDBFromPool(pool)
	defer db.Close()
	v, err := goose.GetDBVersionContext(ctx, db)
	if err != nil {
		return 0, fmt.Errorf("migrate: read version: %w", err)
	}
	return v, nil
}

// EnsureCurrent fails when the database is behind the embedded migrations.
// The service must not serve a schema older than the code that expects it.
func EnsureCurrent(ctx context.Context, pool *pgxpool.Pool, migrations fs.FS) error {
	want, err := Latest(migrations)
	if err != nil {
		return err
	}
	got, err := Version(ctx, pool)
	if err != nil {
		return err
	}
	if got < want {
		return fmt.Errorf("database is at version %d, migrations are at %d: apply them before serving", got, want)
	}
	return nil
}

var versionPrefix = regexp.MustCompile(`^(\d+)_`)

// Latest returns the highest migration version in migrations.
func Latest(migrations fs.FS) (int64, error) {
	entries, err := fs.ReadDir(migrations, Dir)
	if err != nil {
		return 0, fmt.Errorf("migrate: read %s: %w", Dir, err)
	}
	var latest int64
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".sql") {
			continue
		}
		m := versionPrefix.FindStringSubmatch(e.Name())
		if m == nil {
			continue
		}
		n, err := strconv.ParseInt(m[1], 10, 64)
		if err == nil && n > latest {
			latest = n
		}
	}
	if latest == 0 {
		return 0, fmt.Errorf("migrate: no migrations in %s", Dir)
	}
	return latest, nil
}

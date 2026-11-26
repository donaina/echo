// Package auth holds the auth service's database migrations.
package auth

import "embed"

// Migrations is the auth service's migration filesystem.
//
//go:embed migrations/*.sql
var Migrations embed.FS

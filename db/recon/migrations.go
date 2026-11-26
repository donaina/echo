// Package recon holds the reconciliation service's database migrations.
package recon

import "embed"

// Migrations is the reconciliation service's migration filesystem.
//
//go:embed migrations/*.sql
var Migrations embed.FS

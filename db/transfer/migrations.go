// Package transfer holds the transfer service's database migrations.
package transfer

import "embed"

// Migrations is the transfer service's migration filesystem.
//
//go:embed migrations/*.sql
var Migrations embed.FS

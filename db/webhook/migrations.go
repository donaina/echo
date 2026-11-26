// Package webhook holds the webhook service's database migrations.
package webhook

import "embed"

// Migrations is the webhook service's migration filesystem.
//
//go:embed migrations/*.sql
var Migrations embed.FS

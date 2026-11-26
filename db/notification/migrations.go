// Package notification holds the notification service's database migrations.
package notification

import "embed"

// Migrations is the notification service's migration filesystem.
//
//go:embed migrations/*.sql
var Migrations embed.FS

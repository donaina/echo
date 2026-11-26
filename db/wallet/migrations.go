// Package wallet holds the wallet service's database migrations. They are
// embedded in the binary so a release carries the schema it expects.
package wallet

import "embed"

// Migrations is the wallet service's migration filesystem.
//
//go:embed migrations/*.sql
var Migrations embed.FS

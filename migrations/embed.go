// Package migrations embeds the SQL schema migrations applied at boot by
// internal/store. Files are named NNNN_slug.sql and applied in filename order.
package migrations

import "embed"

// FS holds every *.sql migration in this directory.
//
//go:embed *.sql
var FS embed.FS

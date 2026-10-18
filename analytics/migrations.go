// Package analytics holds the ANALYTICAL database's schema (ADR 0008): the
// golang-migrate files under migrations/, embedded so cmd/ti-projector applies
// them without a migrations directory in the image (the same approach as the
// OLTP migrations in the top-level migrations package). The analytical
// database is separate from the OLTP one; it is written only by
// cmd/ti-projector and read (read-only) by cmd/ti-reports.
package analytics

import "embed"

// Migrations is the analytical schema: migrations/*.{up,down}.sql.
//
//go:embed migrations/*.sql
var Migrations embed.FS

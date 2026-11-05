// Package migrations embeds the state schema migrations. Files are applied in
// name order; "{{schema}}" is replaced with the configured schema name.
package migrations

import "embed"

//go:embed postgres/*.sql
var Postgres embed.FS

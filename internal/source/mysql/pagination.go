package mysql

import (
	"fmt"

	"github.com/hyperbolic-c/newapi2ACG/internal/config"
	"github.com/hyperbolic-c/newapi2ACG/internal/event"
)

// quote wraps an identifier already validated by config (plain names only).
func quote(ident string) string { return "`" + ident + "`" }

// pageQuery builds a keyset page over the primary key (design-core §4.3).
// Both forms take arguments so every page uses the binary protocol: mixing
// protocols would represent the same value differently (for example FLOAT).
func pageQuery(d config.Dataset, cur event.Cursor, limit int) (string, []any) {
	if !cur.Started {
		return fmt.Sprintf("SELECT * FROM %s ORDER BY %s LIMIT ?", quote(d.Table), quote(d.PrimaryKey)), []any{limit}
	}
	return fmt.Sprintf("SELECT * FROM %s WHERE %s > ? ORDER BY %s LIMIT ?", quote(d.Table), quote(d.PrimaryKey), quote(d.PrimaryKey)),
		[]any{cur.Value, limit}
}

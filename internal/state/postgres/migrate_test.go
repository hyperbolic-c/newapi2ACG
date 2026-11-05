package postgres

import (
	"strings"
	"testing"
)

func TestSchemaValidation(t *testing.T) {
	for _, ok := range []string{"migration", "acg_migrator", "_x1"} {
		if err := validSchema(ok); err != nil {
			t.Errorf("%s rejected: %v", ok, err)
		}
	}
	for _, bad := range []string{"", "Migration", "a;drop", "a.b", "1abc"} {
		if validSchema(bad) == nil {
			t.Errorf("%q accepted", bad)
		}
	}
}

func TestMigrationsRenderSchema(t *testing.T) {
	ms, err := loadMigrations("migration")
	if err != nil || len(ms) == 0 {
		t.Fatalf("migrations: %v %d", err, len(ms))
	}
	all := ""
	for _, m := range ms {
		all += m.sql
	}
	if strings.Contains(all, "{{schema}}") {
		t.Fatal("schema placeholder left in SQL")
	}
	for _, table := range []string{"migration_tasks", "migration_checkpoints", "migration_resource_map"} {
		if !strings.Contains(all, "CREATE TABLE migration."+table+" (") {
			t.Errorf("missing table %s", table)
		}
	}
}

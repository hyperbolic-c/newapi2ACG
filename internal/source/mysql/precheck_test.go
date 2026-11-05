package mysql

import (
	"strings"
	"testing"
)

func goodFacts() facts {
	return facts{
		vars: map[string]string{"log_bin": "ON", "binlog_format": "ROW", "binlog_row_image": "FULL", "binlog_row_metadata": "FULL",
			"binlog_row_value_options": "", "gtid_mode": "ON", "log_replica_updates": "ON", "server_id": "1"},
		tables: map[string]string{"users": "InnoDB", "tokens": "InnoDB"},
		columns: map[string][]column{
			"users":  {{"id", "bigint"}, {"name", "varchar"}},
			"tokens": {{"id", "bigint"}, {"key", "char"}, {"status", "bigint"}},
		},
		grants: "GRANT RELOAD, REPLICATION SLAVE, REPLICATION CLIENT ON *.* TO `m`@`%`\nGRANT SELECT ON `newapi`.* TO `m`@`%`",
	}
}

func TestEvaluateAcceptsGoodSource(t *testing.T) {
	s, _ := testSource(t, "gtid")
	for _, replica := range []bool{false, true} {
		f := goodFacts()
		f.replica = replica
		if problems, warnings := s.evaluate(f); len(problems) != 0 || len(warnings) != 0 {
			t.Fatalf("replica=%v problems=%v warnings=%v", replica, problems, warnings)
		}
	}
}

func TestEvaluateReportsProblems(t *testing.T) {
	s, _ := testSource(t, "gtid")
	cases := map[string]func(*facts){
		"log_bin":                  func(f *facts) { f.vars["log_bin"] = "OFF" },
		"binlog_format":            func(f *facts) { f.vars["binlog_format"] = "MIXED" },
		"binlog_row_image":         func(f *facts) { f.vars["binlog_row_image"] = "MINIMAL" },
		"binlog_row_metadata":      func(f *facts) { f.vars["binlog_row_metadata"] = "MINIMAL" },
		"binlog_row_value_options": func(f *facts) { f.vars["binlog_row_value_options"] = "PARTIAL_JSON" },
		"gtid_mode":                func(f *facts) { f.vars["gtid_mode"] = "OFF" },
		"log_replica_updates":      func(f *facts) { f.replica = true; f.vars["log_replica_updates"] = "OFF" },
		"server_id":                func(f *facts) { f.vars["server_id"] = "4201" },
		"not found":                func(f *facts) { delete(f.tables, "tokens") },
		"InnoDB":                   func(f *facts) { f.tables["users"] = "MyISAM" },
		"enum":                     func(f *facts) { f.columns["users"] = append(f.columns["users"], column{"role", "enum"}) },
		"primary key":              func(f *facts) { f.columns["users"] = []column{{"uid", "bigint"}} },
		"sensitive column":         func(f *facts) { f.columns["tokens"] = []column{{"id", "bigint"}} },
	}
	for want, edit := range cases {
		f := goodFacts()
		edit(&f)
		problems, _ := s.evaluate(f)
		if len(problems) == 0 || !strings.Contains(strings.Join(problems, "\n"), want) {
			t.Errorf("%s: problems %v", want, problems)
		}
	}
}

func TestEvaluateWarnsOnMissingGrantsAndServerIDInUse(t *testing.T) {
	s, _ := testSource(t, "auto")
	f := goodFacts()
	f.grants = "GRANT SELECT ON `newapi`.* TO `m`@`%`"
	f.replicaIDs = []string{"4201"}
	problems, warnings := s.evaluate(f)
	if len(problems) != 0 || len(warnings) != 4 {
		t.Fatalf("problems=%v warnings=%v", problems, warnings)
	}
}

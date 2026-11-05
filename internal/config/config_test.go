package config

import (
	"strings"
	"testing"
	"time"
)

func TestExampleConfigLoads(t *testing.T) {
	cfg, err := Load("../../configs/jobs/newapi-to-acg.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Job.ID != "newapi-to-acg" || len(cfg.Source.Datasets) != 3 || cfg.Source.Datasets[1].SensitiveColumns[0] != "key" {
		t.Fatalf("unexpected config %+v", cfg)
	}
	if cfg.Sync.Reconcile.Interval.D() != 24*time.Hour || cfg.Hash() == "" {
		t.Fatal("durations or hash wrong")
	}
}

const minimal = `
version: 1
job: {id: j1}
source:
  type: mysql
  dsn_env: SRC_DSN
  replication: {server_id: 7}
  datasets: [{name: users, table: users, primary_key: id}]
target: {type: mock}
state: {dsn_env: PG_DSN, hash_key_env: HASH_KEY}
mapping: {profile: passthrough}
`

func TestDefaults(t *testing.T) {
	cfg, err := Parse([]byte(minimal))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Sync.Full.BatchSize != 500 || cfg.Sync.Incremental.MaxRetries != 5 || cfg.Sync.Reconcile.DeletePolicy != "report" ||
		cfg.State.Schema != "migration" || cfg.Job.LeaseTTL.D() != 30*time.Second || cfg.Source.Replication.PositionMode != "auto" {
		t.Fatalf("defaults %+v", cfg)
	}
}

func TestStrictParsing(t *testing.T) {
	for name, doc := range map[string]string{
		"unknown field":  minimal + "extra: 1\n",
		"two documents":  minimal + "---\nversion: 1\n",
		"duplicate key":  minimal + "version: 1\n",
		"bad duration":   strings.Replace(minimal, "job: {id: j1}", "job: {id: j1, lease_ttl: 30}", 1),
		"empty document": "",
	} {
		if _, err := Parse([]byte(doc)); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestValidation(t *testing.T) {
	for name, edit := range map[string][2]string{
		"literal dsn":       {"dsn_env: SRC_DSN", "dsn_env: user:pw@h:3306/db"},
		"no server id":      {"server_id: 7", "server_id: 0"},
		"bad table":         {"table: users", "table: users;drop"},
		"duplicate dataset": {"datasets: [{name: users, table: users, primary_key: id}]", "datasets: [{name: users, table: users, primary_key: id}, {name: users, table: u2, primary_key: id}]"},
		"pk sensitive":      {"primary_key: id}", "primary_key: id, sensitive_columns: [id]}"},
		"bad target":        {"target: {type: mock}", "target: {type: file}"},
		"acg without url":   {"target: {type: mock}", "target: {type: acg-api, token_env: TOKEN}"},
		"no hash key":       {"hash_key_env: HASH_KEY", "hash_key_env: ''"},
		"bad schema":        {"state: {dsn_env", "state: {schema: Bad-Name, dsn_env"},
		"no profile":        {"profile: passthrough", "profile: ''"},
	} {
		doc := strings.Replace(minimal, edit[0], edit[1], 1)
		if doc == minimal {
			t.Fatalf("%s: edit did not apply", name)
		}
		if _, err := Parse([]byte(doc)); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestBaseURLExpansion(t *testing.T) {
	t.Setenv("ACG_URL_TEST", "https://acg.example")
	doc := strings.Replace(minimal, "target: {type: mock}", "target: {type: acg-api, base_url: '${ACG_URL_TEST}/api', token_env: TOKEN}", 1)
	cfg, err := Parse([]byte(doc))
	if err != nil || cfg.Target.BaseURL != "https://acg.example/api" {
		t.Fatalf("%v %v", cfg, err)
	}
}

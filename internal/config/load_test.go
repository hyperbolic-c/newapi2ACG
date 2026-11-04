package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const jobYAML = `job:
  source: example
  mapping: newapi-to-acg
  target: memory-test
`

func TestUnknownFieldRejected(t *testing.T) {
	_, err := LoadFiles(map[string][]byte{"jobs/example.yaml": []byte(jobYAML + "policies:\n  typo: fail\n")})
	if err == nil || !strings.Contains(err.Error(), "jobs/example.yaml") || !strings.Contains(err.Error(), "policies.typo") {
		t.Fatalf("expected file and field path, got %v", err)
	}
}
func TestPolicyEnumValidated(t *testing.T) {
	for _, field := range []string{"on_source_delete", "prune", "drift", "exhausted_token", "cross_instance_conflict"} {
		_, err := LoadFiles(map[string][]byte{"jobs/example.yaml": []byte(jobYAML + "policies:\n  " + field + ": not-a-policy\n")})
		if err == nil || !strings.Contains(err.Error(), "policies."+field) {
			t.Fatalf("%s: %v", field, err)
		}
	}
}
func TestSecretsOnlyByEnvName(t *testing.T) {
	for _, yaml := range []string{
		"instance: prod\nproject: new-api\naccess:\n  type: database\n  driver: mysql\n  dsn: secret-should-not-leak\n",
		"instance: prod\nproject: new-api\naccess:\n  type: database\n  driver: mysql\n  dsn_env: mysql://user:secret-should-not-leak@host/db\n",
	} {
		_, err := LoadFiles(map[string][]byte{"instances/prod.yaml": []byte(yaml)})
		if err == nil {
			t.Fatal("accepted literal credential")
		}
		if strings.Contains(err.Error(), "secret-should-not-leak") {
			t.Fatal("error leaked secret")
		}
	}
}
func TestConfigHashStable(t *testing.T) {
	a, err := LoadFiles(map[string][]byte{"jobs/a.yaml": []byte(jobYAML), "lookups/status.yaml": []byte("1: enabled\n2: disabled\n")})
	if err != nil {
		t.Fatal(err)
	}
	b, err := LoadFiles(map[string][]byte{"lookups/status.yaml": []byte("2: disabled\n1: enabled\n"), "jobs/a.yaml": []byte("# comment\n" + jobYAML)})
	if err != nil {
		t.Fatal(err)
	}
	if a.Hash != b.Hash {
		t.Fatal("file/map order or comments affected hash")
	}
	c, err := LoadFiles(map[string][]byte{"jobs/a.yaml": []byte(jobYAML + "policies:\n  drift: overwrite\n"), "lookups/status.yaml": []byte("1: enabled\n2: disabled\n")})
	if err != nil {
		t.Fatal(err)
	}
	if c.Hash == a.Hash {
		t.Fatal("policy change did not affect hash")
	}
}
func TestPolicyDefaults(t *testing.T) {
	bundle, err := LoadFiles(map[string][]byte{"jobs/example.yaml": []byte(jobYAML)})
	if err != nil {
		t.Fatal(err)
	}
	job := bundle.Jobs["example"]
	if job.Policies.OnSourceDelete != "disable" || job.Policies.ExhaustedToken != "disabled" || job.Policies.Prune != "report" || job.Execution.MaxPending != 50000 || job.Execution.RequestTimeout != "10s" {
		t.Fatalf("wrong defaults: %+v", job)
	}
}
func TestYAMLRejectsAmbiguity(t *testing.T) {
	for _, input := range []string{jobYAML + "job: {}\n", jobYAML + "---\njob: {}\n", jobYAML + "policies: &p {prune: report}\nexecution: *p\n", jobYAML + "execution:\n  batch_size: wrong-secret-value\n", jobYAML + "execution:\n  batch_size: 0\n", jobYAML + "policies: null\n"} {
		_, err := LoadFiles(map[string][]byte{"jobs/a.yaml": []byte(input)})
		if err == nil {
			t.Fatal("accepted ambiguous or invalid YAML")
		}
		if strings.Contains(err.Error(), "wrong-secret-value") {
			t.Fatal("type error leaked input")
		}
	}
}
func TestSourceContractValidation(t *testing.T) {
	input := `version: 2
project: new-api
compat:
  supported: '=1.0.0-rc.21'
  probe:
    database:
      require_columns:
        users: [id]
datasets:
  users:
    primary_key: id
    schema:
      id: {type: int, required: true}
    dropped: [password]
    bindings:
      database: {table: users}
`
	if _, err := LoadFiles(map[string][]byte{"sources/new-api.yaml": []byte(input)}); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadFiles(map[string][]byte{"sources/new-api.yaml": []byte(strings.Replace(input, "dropped: [password]", "dropped: [id]", 1))}); err == nil {
		t.Fatal("accepted selected/dropped overlap")
	}
}
func TestRepositoryExamples(t *testing.T) {
	b, err := LoadDir("../../configs")
	if err != nil {
		t.Fatal(err)
	}
	if b.Sources["new-api"].Project != "new-api" {
		t.Fatal("source contract not loaded")
	}
}

func TestLoadDirRejectsEmptyConfiguration(t *testing.T) {
	if _, err := LoadDir(t.TempDir()); err == nil {
		t.Fatal("empty config directory accepted")
	}
}
func TestEnvironmentDoesNotAffectConfigHash(t *testing.T) {
	files := map[string][]byte{"instances/prod.yaml": []byte("instance: prod\nproject: new-api\naccess:\n  type: database\n  driver: mysql\n  dsn_env: TEST_SOURCE_DSN\n")}
	t.Setenv("TEST_SOURCE_DSN", "first-private-value")
	a, err := LoadFiles(files)
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("TEST_SOURCE_DSN", "second-private-value")
	b, err := LoadFiles(files)
	if err != nil {
		t.Fatal(err)
	}
	if a.Hash != b.Hash {
		t.Fatal("secret environment value affected hash")
	}
}
func TestConfigReferenceResolution(t *testing.T) {
	b, err := LoadDir("../../configs")
	if err != nil {
		t.Fatal(err)
	}
	if err := b.ValidateReferences(); err != nil {
		t.Fatal(err)
	}
	delete(b.Targets, "memory-test")
	if err := b.ValidateReferences(); err == nil {
		t.Fatal("missing target accepted")
	}
}

func TestConfigMapSymlinks(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "..generation", "targets"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "..generation", "targets", "memory-test.yaml"), []byte("target: memory-test\ntype: memory\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("..generation", filepath.Join(root, "..data")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("..data/targets", filepath.Join(root, "targets")); err != nil {
		t.Fatal(err)
	}
	bundle, err := LoadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(bundle.Targets) != 1 {
		t.Fatal("projected config not loaded")
	}
}
func TestConfigSymlinkCannotEscapeRoot(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "targets"), 0700); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(outside, "memory-test.yaml")
	if err := os.WriteFile(target, []byte("target: memory-test\ntype: memory\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(root, "targets", "memory-test.yaml")); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadDir(root); err == nil {
		t.Fatal("escaped config root")
	}
}

func TestSourceDefaultMatchesDeclaredType(t *testing.T) {
	for _, tc := range []struct {
		kind, value string
		valid       bool
	}{
		{"int", "3", true}, {"int", "not-an-integer-secret", false}, {"int", "1.5", false},
		{"bool", "true", true}, {"bool", "'true'", false},
		{"string", "'model'", true}, {"string", "42", false},
		{"timestamp", "'2026-11-04T01:00:00Z'", true}, {"timestamp", "'yesterday'", false},
		{"json", "{nested: [1, true, 'text']}", true}, {"secret", "'must-not-persist'", false},
	} {
		t.Run(tc.kind+"/"+tc.value, func(t *testing.T) {
			input := "version: 2\nproject: source\ncompat:\n  supported: '=1.0.0'\n  probe:\n    database:\n      require_columns:\n        rows: [id]\ndatasets:\n  rows:\n    primary_key: id\n    bindings:\n      database: {table: rows}\n    schema:\n      id: {type: int, required: true}\n      value:\n        type: " + tc.kind + "\n        default: " + tc.value + "\n"
			_, err := LoadFiles(map[string][]byte{"sources/source.yaml": []byte(input)})
			if (err == nil) != tc.valid {
				t.Fatalf("expected valid=%v got %v", tc.valid, err)
			}
			if err != nil && (!strings.Contains(err.Error(), "schema.value.default") || strings.Contains(err.Error(), "not-an-integer-secret")) {
				t.Fatalf("unsafe/imprecise error: %v", err)
			}
		})
	}
}

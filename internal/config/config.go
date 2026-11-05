// Package config loads one job configuration file (design-core §10). Values
// that are secrets are never in the file: only the names of environment
// variables that hold them.
package config

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"time"

	"go.yaml.in/yaml/v3"
)

type Config struct {
	Version int     `yaml:"version" json:"version"`
	Job     Job     `yaml:"job" json:"job"`
	Source  Source  `yaml:"source" json:"source"`
	Sync    Sync    `yaml:"sync" json:"sync"`
	Target  Target  `yaml:"target" json:"target"`
	State   State   `yaml:"state" json:"state"`
	Mapping Mapping `yaml:"mapping" json:"mapping"`
}

type Job struct {
	ID       string   `yaml:"id" json:"id"`
	Enabled  bool     `yaml:"enabled" json:"enabled"`
	LeaseTTL Duration `yaml:"lease_ttl" json:"lease_ttl"`
}

type Source struct {
	Type        string      `yaml:"type" json:"type"`
	DSNEnv      string      `yaml:"dsn_env" json:"dsn_env"`
	Replication Replication `yaml:"replication" json:"replication"`
	Datasets    []Dataset   `yaml:"datasets" json:"datasets"`
}

type Replication struct {
	ServerID     uint32 `yaml:"server_id" json:"server_id"`
	PositionMode string `yaml:"position_mode" json:"position_mode"` // auto | gtid | filepos
}

type Dataset struct {
	Name       string `yaml:"name" json:"name"`
	Table      string `yaml:"table" json:"table"`
	PrimaryKey string `yaml:"primary_key" json:"primary_key"`
	// SensitiveColumns hold credentials (API keys, channel keys). Their
	// values are carried as secrets.Value and never logged or stored.
	SensitiveColumns []string `yaml:"sensitive_columns" json:"sensitive_columns"`
}

type Sync struct {
	Full        Full        `yaml:"full" json:"full"`
	Incremental Incremental `yaml:"incremental" json:"incremental"`
	Reconcile   Reconcile   `yaml:"reconcile" json:"reconcile"`
}

type Full struct {
	Enabled    bool `yaml:"enabled" json:"enabled"`
	OnFirstRun bool `yaml:"on_first_run" json:"on_first_run"`
	// BatchSize is rows per snapshot read.
	BatchSize int `yaml:"batch_size" json:"batch_size"`
	// LockWaitTimeout bounds waiting for the brief global read lock taken to
	// align the snapshot with its binlog position.
	LockWaitTimeout Duration `yaml:"lock_wait_timeout" json:"lock_wait_timeout"`
}

type Incremental struct {
	Enabled bool `yaml:"enabled" json:"enabled"`
	// BatchSize is the maximum operations per target Apply call. A source
	// transaction larger than this is applied in several calls, and the
	// checkpoint still advances only after all of them succeed.
	BatchSize  int `yaml:"batch_size" json:"batch_size"`
	MaxRetries int `yaml:"max_retries" json:"max_retries"`
}

type Reconcile struct {
	Enabled   bool     `yaml:"enabled" json:"enabled"`
	Interval  Duration `yaml:"interval" json:"interval"`
	BatchSize int      `yaml:"batch_size" json:"batch_size"`
	// DeletePolicy handles target resources whose source row no longer
	// exists: "report" logs them, "delete" removes them. Only resources this
	// task created are ever considered (design-core §6.4).
	DeletePolicy string `yaml:"delete_policy" json:"delete_policy"`
}

type Target struct {
	Type     string   `yaml:"type" json:"type"` // acg-api | mock
	BaseURL  string   `yaml:"base_url" json:"base_url"`
	TokenEnv string   `yaml:"token_env" json:"token_env"`
	Timeout  Duration `yaml:"timeout" json:"timeout"`
}

type State struct {
	Type   string `yaml:"type" json:"type"`
	DSNEnv string `yaml:"dsn_env" json:"dsn_env"`
	Schema string `yaml:"schema" json:"schema"`
	// HashKeyEnv names the HMAC key used for resource hashes (design-core §8.3).
	HashKeyEnv string `yaml:"hash_key_env" json:"hash_key_env"`
}

type Mapping struct {
	Profile string `yaml:"profile" json:"profile"`
}

// Duration reads "30s", "24h" and so on.
type Duration time.Duration

func (d *Duration) UnmarshalYAML(n *yaml.Node) error {
	var s string
	if err := n.Decode(&s); err != nil {
		return fmt.Errorf("line %d: duration must be a string such as \"30s\"", n.Line)
	}
	v, err := time.ParseDuration(s)
	if err != nil {
		return fmt.Errorf("line %d: invalid duration %q", n.Line, s)
	}
	*d = Duration(v)
	return nil
}

func (d Duration) MarshalJSON() ([]byte, error) { return json.Marshal(time.Duration(d).String()) }
func (d Duration) D() time.Duration             { return time.Duration(d) }

// Load reads, defaults and validates a job file. ${VAR} in target.base_url
// is expanded from the environment.
func Load(path string) (*Config, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read config: %w", err)
	}
	return Parse(raw)
}

func Parse(raw []byte) (*Config, error) {
	cfg := defaults()
	dec := yaml.NewDecoder(bytes.NewReader(raw))
	dec.KnownFields(true)
	if err := dec.Decode(cfg); err != nil {
		if errors.Is(err, io.EOF) {
			return nil, errors.New("config is empty")
		}
		return nil, fmt.Errorf("config: %w", err)
	}
	var extra any
	if err := dec.Decode(&extra); !errors.Is(err, io.EOF) {
		return nil, errors.New("config must contain exactly one YAML document")
	}
	cfg.Target.BaseURL = os.ExpandEnv(cfg.Target.BaseURL)
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	return cfg, nil
}

func defaults() *Config {
	return &Config{
		Job:    Job{Enabled: true, LeaseTTL: Duration(30 * time.Second)},
		Source: Source{Replication: Replication{PositionMode: "auto"}},
		Sync: Sync{
			Full:        Full{Enabled: true, OnFirstRun: true, BatchSize: 500, LockWaitTimeout: Duration(10 * time.Second)},
			Incremental: Incremental{Enabled: true, BatchSize: 100, MaxRetries: 5},
			Reconcile:   Reconcile{Enabled: true, Interval: Duration(24 * time.Hour), BatchSize: 500, DeletePolicy: "report"},
		},
		Target: Target{Timeout: Duration(30 * time.Second)},
		State:  State{Type: "postgres", Schema: "migration"},
	}
}

// Hash identifies the effective configuration; a change triggers a
// reconciliation so new mapping rules are applied (design-core §6.1).
func (c *Config) Hash() string {
	b, _ := json.Marshal(c)
	return fmt.Sprintf("%x", sha256.Sum256(b))
}

// DatasetNames returns dataset names in configured order.
func (c *Config) DatasetNames() []string {
	out := make([]string, len(c.Source.Datasets))
	for i, d := range c.Source.Datasets {
		out[i] = d.Name
	}
	return out
}

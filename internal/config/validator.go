package config

import (
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"slices"
	"time"
)

var (
	idPattern    = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,62}$`)
	envPattern   = regexp.MustCompile(`^[A-Z_][A-Z0-9_]*$`)
	identPattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]{0,63}$`)
)

func (c *Config) Validate() error {
	var errs []error
	bad := func(field, format string, args ...any) {
		errs = append(errs, fmt.Errorf("%s: %s", field, fmt.Sprintf(format, args...)))
	}
	env := func(field, v string) {
		if !envPattern.MatchString(v) {
			bad(field, "must name an environment variable (upper case), not contain a value")
		}
	}
	if c.Version != 1 {
		bad("version", "must be 1")
	}
	if !idPattern.MatchString(c.Job.ID) {
		bad("job.id", "must match %s", idPattern)
	}
	if c.Job.LeaseTTL.D() < 5*time.Second {
		bad("job.lease_ttl", "must be at least 5s")
	}

	s := c.Source
	if s.Type != "mysql" {
		bad("source.type", "only mysql is supported")
	}
	env("source.dsn_env", s.DSNEnv)
	if s.Replication.ServerID == 0 {
		bad("source.replication.server_id", "required; must be unique among replicas of the source")
	}
	if !slices.Contains([]string{"auto", "gtid", "filepos"}, s.Replication.PositionMode) {
		bad("source.replication.position_mode", "must be auto, gtid or filepos")
	}
	if len(s.Datasets) == 0 {
		bad("source.datasets", "at least one dataset is required")
	}
	seen := map[string]bool{}
	for i, d := range s.Datasets {
		p := fmt.Sprintf("source.datasets[%d]", i)
		if !idPattern.MatchString(d.Name) {
			bad(p+".name", "must match %s", idPattern)
		} else if seen[d.Name] {
			bad(p+".name", "duplicate dataset %q", d.Name)
		}
		seen[d.Name] = true
		if !identPattern.MatchString(d.Table) {
			bad(p+".table", "must be a plain table name")
		}
		if !identPattern.MatchString(d.PrimaryKey) {
			bad(p+".primary_key", "must be a single column name")
		}
		for _, col := range d.SensitiveColumns {
			if !identPattern.MatchString(col) {
				bad(p+".sensitive_columns", "invalid column %q", col)
			}
			if col == d.PrimaryKey {
				bad(p+".sensitive_columns", "the primary key cannot be sensitive")
			}
		}
	}

	f, in, r := c.Sync.Full, c.Sync.Incremental, c.Sync.Reconcile
	if f.BatchSize < 1 || f.BatchSize > 100000 {
		bad("sync.full.batch_size", "must be between 1 and 100000")
	}
	if f.LockWaitTimeout.D() < time.Second {
		bad("sync.full.lock_wait_timeout", "must be at least 1s")
	}
	if in.BatchSize < 1 || in.BatchSize > 100000 {
		bad("sync.incremental.batch_size", "must be between 1 and 100000")
	}
	if in.MaxRetries < 0 {
		bad("sync.incremental.max_retries", "must not be negative")
	}
	if r.Enabled && r.Interval.D() < time.Minute {
		bad("sync.reconcile.interval", "must be at least 1m")
	}
	if r.BatchSize < 1 || r.BatchSize > 100000 {
		bad("sync.reconcile.batch_size", "must be between 1 and 100000")
	}
	if !slices.Contains([]string{"report", "delete"}, r.DeletePolicy) {
		bad("sync.reconcile.delete_policy", "must be report or delete")
	}

	t := c.Target
	switch t.Type {
	case "acg-api":
		u, err := url.Parse(t.BaseURL)
		if t.BaseURL == "" || err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
			bad("target.base_url", "must be an http(s) URL (after ${VAR} expansion)")
		}
		env("target.token_env", t.TokenEnv)
	case "mock":
	default:
		bad("target.type", "must be acg-api or mock")
	}
	if t.Timeout.D() <= 0 {
		bad("target.timeout", "must be positive")
	}

	st := c.State
	if st.Type != "postgres" {
		bad("state.type", "only postgres is supported")
	}
	env("state.dsn_env", st.DSNEnv)
	env("state.hash_key_env", st.HashKeyEnv)
	if !regexp.MustCompile(`^[a-z_][a-z0-9_]{0,62}$`).MatchString(st.Schema) {
		bad("state.schema", "must be a lower-case identifier")
	}
	if c.Mapping.Profile == "" {
		bad("mapping.profile", "required")
	}
	return errors.Join(errs...)
}

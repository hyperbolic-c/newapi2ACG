package config

import (
	"encoding/json"
	"fmt"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
)

var envPattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
var namePattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_-]*$`)

func (j Job) Validate() error {
	for _, field := range []struct{ path, value string }{{"job.source", j.Job.Source}, {"job.mapping", j.Job.Mapping}, {"job.target", j.Job.Target}} {
		if !namePattern.MatchString(field.value) {
			return fmt.Errorf("%s: valid reference name required", field.path)
		}
	}
	for _, field := range []struct {
		path, value string
		allowed     []string
	}{
		{"policies.conflict", j.Policies.Conflict, []string{"fail", "skip", "update"}},
		{"policies.unsupported", j.Policies.Unsupported, []string{"fail"}},
		{"policies.missing_reference", j.Policies.MissingReference, []string{"fail"}},
		{"policies.missing_secret", j.Policies.MissingSecret, []string{"fail"}},
		{"policies.drift", j.Policies.Drift, []string{"report", "overwrite"}},
		{"policies.on_source_delete", j.Policies.OnSourceDelete, []string{"disable", "report"}},
		{"policies.prune", j.Policies.Prune, []string{"report", "delete"}},
		{"policies.auto_disabled_channel", j.Policies.AutoDisabledChannel, []string{"disabled", "enabled"}},
		{"policies.exhausted_token", j.Policies.ExhaustedToken, []string{"disabled", "enabled"}},
		{"policies.direct_routes", j.Policies.DirectRoutes, []string{"none", "group_channels"}},
		{"policies.cross_instance_conflict", j.Policies.CrossInstanceConflict, []string{"fail"}},
	} {
		if !slices.Contains(field.allowed, field.value) {
			return fmt.Errorf("%s: unsupported policy value", field.path)
		}
	}
	for _, field := range []struct{ path, value string }{{"reconcile.interval", j.Reconcile.Interval}, {"execution.request_timeout", j.Execution.RequestTimeout}} {
		d, err := time.ParseDuration(field.value)
		if err != nil || d <= 0 {
			return fmt.Errorf("%s: positive duration required", field.path)
		}
	}
	if len(j.Execution.RetryBackoff) == 0 {
		return fmt.Errorf("execution.retry_backoff: at least one delay required")
	}
	for i, value := range j.Execution.RetryBackoff {
		d, err := time.ParseDuration(value)
		if err != nil || d <= 0 {
			return fmt.Errorf("execution.retry_backoff[%d]: positive duration required", i)
		}
	}
	for _, field := range []struct {
		path  string
		value int
	}{{"execution.batch_size", j.Execution.BatchSize}, {"execution.max_pending", j.Execution.MaxPending}, {"execution.fanout_alert", j.Execution.FanoutAlert}} {
		if field.value <= 0 {
			return fmt.Errorf("%s: positive integer required", field.path)
		}
	}
	if !strings.HasSuffix(j.Execution.WriteRateLimit, "/s") {
		return fmt.Errorf("execution.write_rate_limit: positive integer/s required")
	}
	rate, err := strconv.ParseUint(strings.TrimSuffix(j.Execution.WriteRateLimit, "/s"), 10, 32)
	if err != nil || rate == 0 {
		return fmt.Errorf("execution.write_rate_limit: positive integer/s required")
	}
	return nil
}
func envName(field, value string, required bool) error {
	if value == "" && !required {
		return nil
	}
	if !envPattern.MatchString(value) {
		return fmt.Errorf("%s: environment variable name required", field)
	}
	return nil
}
func baseURL(field, value string) error {
	u, err := url.Parse(value)
	if err != nil || u.Host == "" || (u.Scheme != "https" && u.Scheme != "http") || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return fmt.Errorf("%s: HTTP(S) URL without credentials, query or fragment required", field)
	}
	return nil
}
func (i Instance) Validate() error {
	if !namePattern.MatchString(i.Instance) || !namePattern.MatchString(i.Project) {
		return fmt.Errorf("instance/project: valid names required")
	}
	switch i.Access.Type {
	case "database":
		if !slices.Contains([]string{"mysql", "postgres", "sqlite"}, i.Access.Driver) {
			return fmt.Errorf("access.driver: unsupported database driver")
		}
		if i.Access.BaseURL != "" || i.Access.CredentialEnv != "" {
			return fmt.Errorf("access: API settings are invalid for database access")
		}
		return envName("access.dsn_env", i.Access.DSNEnv, true)
	case "api":
		if i.Access.Driver != "" || i.Access.DSNEnv != "" {
			return fmt.Errorf("access: database settings are invalid for API access")
		}
		if err := baseURL("access.base_url", i.Access.BaseURL); err != nil {
			return err
		}
		return envName("access.credential_env", i.Access.CredentialEnv, true)
	default:
		return fmt.Errorf("access.type: unsupported access type")
	}
}
func (t Target) Validate() error {
	if !namePattern.MatchString(t.Target) {
		return fmt.Errorf("target: valid name required")
	}
	if !slices.Contains([]string{"memory", "file", "acg"}, t.Type) {
		return fmt.Errorf("type: unsupported target type")
	}
	if err := envName("state_dsn_env", t.StateDSNEnv, false); err != nil {
		return err
	}
	if t.Type == "acg" {
		if err := baseURL("base_url", t.BaseURL); err != nil {
			return err
		}
		if err := envName("credential_env", t.CredentialEnv, true); err != nil {
			return err
		}
	} else if t.BaseURL != "" || t.CredentialEnv != "" {
		return fmt.Errorf("target: remote credentials are only valid for acg")
	}
	if t.Type == "file" && strings.TrimSpace(t.OutputDir) == "" {
		return fmt.Errorf("output_dir: file target requires an output directory")
	}
	if t.Type != "file" && t.OutputDir != "" {
		return fmt.Errorf("output_dir: only valid for file target")
	}
	for _, status := range t.Capabilities {
		if !slices.Contains([]string{"supported", "unsupported", "unknown"}, status) {
			return fmt.Errorf("capabilities: expected supported, unsupported or unknown")
		}
	}
	return nil
}
func (s Source) Validate() error {
	if s.Version != 2 {
		return fmt.Errorf("version: source contract version must be 2")
	}
	if !namePattern.MatchString(s.Project) {
		return fmt.Errorf("project: valid name required")
	}
	if strings.TrimSpace(s.Compat.Supported) == "" {
		return fmt.Errorf("compat.supported: version constraint required")
	}
	if len(s.Datasets) == 0 {
		return fmt.Errorf("datasets: at least one dataset required")
	}
	for name, d := range s.Datasets {
		if !namePattern.MatchString(name) {
			return fmt.Errorf("datasets: invalid dataset name")
		}
		prefix := "datasets." + name
		if !namePattern.MatchString(d.Bindings.Database.Table) {
			return fmt.Errorf("%s.bindings.database.table: valid table name required", prefix)
		}
		pk, ok := d.Schema[d.PrimaryKey]
		if !ok || !pk.Required || pk.Nullable {
			return fmt.Errorf("%s.primary_key: must reference a required nonnullable field", prefix)
		}
		for field, decl := range d.Schema {
			if !namePattern.MatchString(field) {
				return fmt.Errorf("%s.schema: invalid field name", prefix)
			}
			if !slices.Contains([]string{"int", "bool", "string", "json", "timestamp", "secret"}, decl.Type) {
				return fmt.Errorf("%s.schema.%s.type: unsupported contract type", prefix, field)
			}
			if !validDefault(decl) {
				return fmt.Errorf("%s.schema.%s.default: value must match declared type; secret defaults are forbidden", prefix, field)
			}
		}
		seen := map[string]bool{}
		for _, field := range d.Dropped {
			if !namePattern.MatchString(field) {
				return fmt.Errorf("%s.dropped: invalid field name", prefix)
			}
			if _, ok := d.Schema[field]; ok {
				return fmt.Errorf("%s.dropped: overlaps schema", prefix)
			}
			if seen[field] {
				return fmt.Errorf("%s.dropped: duplicate field", prefix)
			}
			seen[field] = true
		}
		columns, ok := s.Compat.Probe.Database.RequireColumns[name]
		if !ok {
			return fmt.Errorf("compat.probe.database.require_columns.%s: required", name)
		}
		seen = map[string]bool{}
		for _, column := range columns {
			if _, ok := d.Schema[column]; !ok {
				return fmt.Errorf("compat.probe.database.require_columns.%s: undeclared field", name)
			}
			if seen[column] {
				return fmt.Errorf("compat.probe.database.require_columns.%s: duplicate field", name)
			}
			seen[column] = true
		}
		for column, field := range d.Schema {
			if field.Required && !seen[column] {
				return fmt.Errorf("compat.probe.database.require_columns.%s: missing required field", name)
			}
		}
	}
	for name := range s.Compat.Probe.Database.RequireColumns {
		if _, ok := s.Datasets[name]; !ok {
			return fmt.Errorf("compat.probe.database.require_columns: unknown dataset")
		}
	}
	return nil
}

func validDefault(field Field) bool {
	if field.Default == nil {
		return true
	}
	switch field.Type {
	case "int":
		switch field.Default.(type) {
		case int, int64:
			return true
		}
	case "bool":
		_, ok := field.Default.(bool)
		return ok
	case "string":
		_, ok := field.Default.(string)
		return ok
	case "timestamp":
		value, ok := field.Default.(string)
		if !ok {
			return false
		}
		_, err := time.Parse(time.RFC3339Nano, value)
		return err == nil
	case "json":
		_, err := json.Marshal(field.Default)
		return err == nil
	}
	return false
}

// ValidateReferences checks the namespaces already implemented by M1. Mapping
// compilation and target capability checks belong to later precheck phases.
func (b *Bundle) ValidateReferences() error {
	for name, instance := range b.Instances {
		if _, ok := b.Sources[instance.Project]; !ok {
			return fmt.Errorf("instances/%s: project references an unknown source contract", name)
		}
	}
	for name, job := range b.Jobs {
		if _, ok := b.Instances[job.Job.Source]; !ok {
			return fmt.Errorf("jobs/%s: job.source references an unknown instance", name)
		}
		if _, ok := b.Targets[job.Job.Target]; !ok {
			return fmt.Errorf("jobs/%s: job.target references an unknown target", name)
		}
	}
	return nil
}

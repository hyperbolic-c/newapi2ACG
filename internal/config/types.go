// Package config loads deterministic, strictly typed migration configuration.
package config

type Bundle struct {
	Hash      string
	Jobs      map[string]Job
	Sources   map[string]Source
	Instances map[string]Instance
	Targets   map[string]Target
	Lookups   map[string]map[string]any
}
type Job struct {
	Job       JobRefs   `yaml:"job" json:"job"`
	Policies  Policies  `yaml:"policies" json:"policies"`
	Reconcile Reconcile `yaml:"reconcile" json:"reconcile"`
	Execution Execution `yaml:"execution" json:"execution"`
}
type JobRefs struct {
	Source  string `yaml:"source" json:"source"`
	Mapping string `yaml:"mapping" json:"mapping"`
	Target  string `yaml:"target" json:"target"`
	DryRun  bool   `yaml:"dry_run" json:"dry_run"`
}
type Policies struct {
	Conflict              string `yaml:"conflict" json:"conflict"`
	Unsupported           string `yaml:"unsupported" json:"unsupported"`
	MissingReference      string `yaml:"missing_reference" json:"missing_reference"`
	MissingSecret         string `yaml:"missing_secret" json:"missing_secret"`
	Drift                 string `yaml:"drift" json:"drift"`
	OnSourceDelete        string `yaml:"on_source_delete" json:"on_source_delete"`
	Prune                 string `yaml:"prune" json:"prune"`
	AutoDisabledChannel   string `yaml:"auto_disabled_channel" json:"auto_disabled_channel"`
	ExhaustedToken        string `yaml:"exhausted_token" json:"exhausted_token"`
	DirectRoutes          string `yaml:"direct_routes" json:"direct_routes"`
	CrossInstanceConflict string `yaml:"cross_instance_conflict" json:"cross_instance_conflict"`
}
type Reconcile struct {
	Interval string `yaml:"interval" json:"interval"`
}
type Execution struct {
	BatchSize      int      `yaml:"batch_size" json:"batch_size"`
	RetryBackoff   []string `yaml:"retry_backoff" json:"retry_backoff"`
	WriteRateLimit string   `yaml:"write_rate_limit" json:"write_rate_limit"`
	RequestTimeout string   `yaml:"request_timeout" json:"request_timeout"`
	MaxPending     int      `yaml:"max_pending" json:"max_pending"`
	FanoutAlert    int      `yaml:"fanout_alert" json:"fanout_alert"`
}
type Instance struct {
	Instance string `yaml:"instance" json:"instance"`
	Project  string `yaml:"project" json:"project"`
	Access   Access `yaml:"access" json:"access"`
}
type Access struct {
	Type          string `yaml:"type" json:"type"`
	Driver        string `yaml:"driver" json:"driver"`
	DSNEnv        string `yaml:"dsn_env" json:"dsn_env"`
	BaseURL       string `yaml:"base_url" json:"base_url"`
	CredentialEnv string `yaml:"credential_env" json:"credential_env"`
}
type Target struct {
	Target        string            `yaml:"target" json:"target"`
	Type          string            `yaml:"type" json:"type"`
	BaseURL       string            `yaml:"base_url" json:"base_url"`
	CredentialEnv string            `yaml:"credential_env" json:"credential_env"`
	StateDSNEnv   string            `yaml:"state_dsn_env" json:"state_dsn_env"`
	OutputDir     string            `yaml:"output_dir" json:"output_dir"`
	Capabilities  map[string]string `yaml:"capabilities" json:"capabilities"`
}
type Source struct {
	Version  int                `yaml:"version" json:"version"`
	Project  string             `yaml:"project" json:"project"`
	Compat   Compatibility      `yaml:"compat" json:"compat"`
	Datasets map[string]Dataset `yaml:"datasets" json:"datasets"`
}
type Compatibility struct {
	Supported string `yaml:"supported" json:"supported"`
	Probe     Probe  `yaml:"probe" json:"probe"`
}
type Probe struct {
	Database DatabaseProbe `yaml:"database" json:"database"`
}
type DatabaseProbe struct {
	RequireColumns map[string][]string `yaml:"require_columns" json:"require_columns"`
}
type Dataset struct {
	PrimaryKey string           `yaml:"primary_key" json:"primary_key"`
	Schema     map[string]Field `yaml:"schema" json:"schema"`
	Dropped    []string         `yaml:"dropped" json:"dropped"`
	Bindings   Bindings         `yaml:"bindings" json:"bindings"`
}
type Field struct {
	Type     string `yaml:"type" json:"type"`
	Required bool   `yaml:"required" json:"required"`
	Nullable bool   `yaml:"nullable" json:"nullable"`
	Default  any    `yaml:"default,omitempty" json:"default,omitempty"`
}
type Bindings struct {
	Database TableBinding `yaml:"database" json:"database"`
}
type TableBinding struct {
	Table string `yaml:"table" json:"table"`
}

func defaultJob() Job {
	return Job{Policies: Policies{Conflict: "fail", Unsupported: "fail", MissingReference: "fail", MissingSecret: "fail", Drift: "report", OnSourceDelete: "disable", Prune: "report", AutoDisabledChannel: "disabled", ExhaustedToken: "disabled", DirectRoutes: "none", CrossInstanceConflict: "fail"}, Reconcile: Reconcile{Interval: "6h"}, Execution: Execution{BatchSize: 100, RetryBackoff: []string{"1s", "5s", "30s", "2m"}, WriteRateLimit: "200/s", RequestTimeout: "10s", MaxPending: 50000, FanoutAlert: 1000}}
}

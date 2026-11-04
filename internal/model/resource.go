package model

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/netip"
	"net/url"
	"regexp"
	"strings"
	"time"
)

// Metadata is persisted independently of target-specific identifiers.
type Metadata struct {
	SourceIdentity Identity `json:"source_identity"`
	MigrationJob   string   `json:"migration_job"`
	ContentHash    string   `json:"content_hash,omitempty"`
}

// SecretRef contains only an HMAC fingerprint, never the original credential.
// A target writer resolves the credential through the source and verifies it.
type SecretRef struct {
	Fingerprint string `json:"fingerprint"`
}

type Group struct {
	Name string `json:"name"`
}
type Provider struct {
	Name         string            `json:"name"`
	DisplayName  string            `json:"display_name,omitempty"`
	Type         string            `json:"type"`
	Endpoint     string            `json:"endpoint"`
	Enabled      bool              `json:"enabled"`
	APIKeys      []SecretRef       `json:"api_keys"`
	ModelMapping map[string]string `json:"model_mapping"`
}
type RouteMember struct {
	ProviderRef Identity `json:"provider_ref"`
	Priority    int64    `json:"priority"`
	Weight      int64    `json:"weight"`
}
type AggregateRoute struct {
	Name    string        `json:"name"`
	Members []RouteMember `json:"members"`
}

// UserInitialization is a create-only action, intentionally not serialized in
// the managed resource or included in its hash. Successful initialization is
// tracked in id_map.password_initialized_at by the future state store.
type UserInitialization struct {
	PasswordEnv        string
	MustChangePassword bool
	ExpiresInSeconds   int64
}
type User struct {
	Username       string              `json:"username"`
	DisplayName    string              `json:"display_name,omitempty"`
	Email          string              `json:"email,omitempty"`
	Role           string              `json:"role"`
	Enabled        bool                `json:"enabled"`
	GroupRef       Identity            `json:"group_ref"`
	Initialization *UserInitialization `json:"-"`
}
type ApiKey struct {
	Name         string     `json:"name"`
	UserRef      Identity   `json:"user_ref"`
	Key          SecretRef  `json:"key"`
	Enabled      bool       `json:"enabled"`
	ExpiresAt    *time.Time `json:"expires_at"`
	IPAllowlist  []string   `json:"ip_allowlist"`
	ProviderRefs []Identity `json:"provider_refs"`
	RouteRefs    []Identity `json:"route_refs"`
}

// Resource is a tagged union. Exactly one payload must match Kind. These are
// logical acg/v1 fields, not a representation of ACG's private storage schema.
type Resource struct {
	APIVersion     string          `json:"api_version"`
	Kind           Kind            `json:"kind"`
	Metadata       Metadata        `json:"metadata"`
	Group          *Group          `json:"group,omitempty"`
	Provider       *Provider       `json:"provider,omitempty"`
	AggregateRoute *AggregateRoute `json:"aggregate_route,omitempty"`
	User           *User           `json:"user,omitempty"`
	ApiKey         *ApiKey         `json:"apikey,omitempty"`
}

var digestPattern = regexp.MustCompile(`^[0-9a-f]{64}$`)

func (r Resource) Validate() error {
	if r.APIVersion != "acg/v1" {
		return errors.New("api_version must be acg/v1")
	}
	if err := r.Metadata.SourceIdentity.Validate(); err != nil {
		return fmt.Errorf("metadata.source_identity: %w", err)
	}
	if r.Metadata.SourceIdentity.Kind != r.Kind {
		return errors.New("kind does not match metadata.source_identity")
	}
	if strings.TrimSpace(r.Metadata.MigrationJob) == "" {
		return errors.New("metadata.migration_job is required")
	}
	if r.Metadata.ContentHash != "" && !digestPattern.MatchString(r.Metadata.ContentHash) {
		return errors.New("metadata.content_hash must be a SHA-256 hex digest")
	}
	n := 0
	for _, set := range []bool{r.Group != nil, r.Provider != nil, r.AggregateRoute != nil, r.User != nil, r.ApiKey != nil} {
		if set {
			n++
		}
	}
	if n != 1 {
		return errors.New("exactly one resource payload is required")
	}
	switch r.Kind {
	case GroupKind:
		if r.Group == nil {
			return errors.New("group payload required")
		}
		return required("group.name", r.Group.Name)
	case ProviderKind:
		if r.Provider == nil {
			return errors.New("provider payload required")
		}
		p := r.Provider
		for _, field := range []struct{ name, value string }{{"provider.name", p.Name}, {"provider.type", p.Type}, {"provider.endpoint", p.Endpoint}} {
			if err := required(field.name, field.value); err != nil {
				return err
			}
		}
		endpoint, err := url.Parse(p.Endpoint)
		if err != nil || endpoint.Host == "" || (endpoint.Scheme != "http" && endpoint.Scheme != "https") || endpoint.User != nil || endpoint.Fragment != "" {
			return errors.New("provider.endpoint must be an HTTP(S) URL without userinfo or fragment")
		}
		if len(p.APIKeys) == 0 {
			return errors.New("provider.api_keys must contain a fingerprint")
		}
		for _, key := range p.APIKeys {
			if !digestPattern.MatchString(key.Fingerprint) {
				return errors.New("provider.api_keys contains an invalid fingerprint")
			}
		}
		for k, v := range p.ModelMapping {
			if strings.TrimSpace(k) == "" || strings.TrimSpace(v) == "" {
				return errors.New("provider.model_mapping requires nonempty model names")
			}
		}
	case AggregateRouteKind:
		if r.AggregateRoute == nil {
			return errors.New("aggregate_route payload required")
		}
		route := r.AggregateRoute
		if err := required("aggregate_route.name", route.Name); err != nil {
			return err
		}
		if len(route.Members) == 0 {
			return errors.New("aggregate_route.members must not be empty")
		}
		seen := map[Identity]RouteMember{}
		for _, m := range route.Members {
			if err := r.validateRef("aggregate_route.members.provider_ref", m.ProviderRef, ProviderKind); err != nil {
				return err
			}
			if m.Weight < 0 {
				return errors.New("aggregate_route.members.weight must be nonnegative")
			}
			if previous, ok := seen[m.ProviderRef]; ok && previous != m {
				return errors.New("aggregate_route.members has conflicting duplicate provider")
			}
			seen[m.ProviderRef] = m
		}
	case UserKind:
		if r.User == nil {
			return errors.New("user payload required")
		}
		if err := required("user.username", r.User.Username); err != nil {
			return err
		}
		if err := required("user.role", r.User.Role); err != nil {
			return err
		}
		return r.validateRef("user.group_ref", r.User.GroupRef, GroupKind)
	case ApiKeyKind:
		if r.ApiKey == nil {
			return errors.New("apikey payload required")
		}
		key := r.ApiKey
		if err := required("apikey.name", key.Name); err != nil {
			return err
		}
		if err := r.validateRef("apikey.user_ref", key.UserRef, UserKind); err != nil {
			return err
		}
		if !digestPattern.MatchString(key.Key.Fingerprint) {
			return errors.New("apikey.key must contain a fingerprint")
		}
		for _, ref := range key.ProviderRefs {
			if err := r.validateRef("apikey.provider_refs", ref, ProviderKind); err != nil {
				return err
			}
		}
		for _, ref := range key.RouteRefs {
			if ref.Kind != AggregateRouteKind && ref.Kind != ProviderKind {
				return errors.New("apikey.route_refs has invalid route reference kind")
			}
			if err := r.validateRef("apikey.route_refs", ref, ref.Kind); err != nil {
				return err
			}
		}
		for _, ip := range key.IPAllowlist {
			if _, err := netip.ParseAddr(ip); err != nil {
				if _, err := netip.ParsePrefix(ip); err != nil {
					return errors.New("apikey.ip_allowlist requires IP addresses or CIDRs")
				}
			}
		}
	default:
		return errors.New("unsupported resource kind")
	}
	return nil
}

func required(path, value string) error {
	if strings.TrimSpace(value) == "" {
		return fmt.Errorf("%s is required", path)
	}
	return nil
}
func (r Resource) validateRef(path string, ref Identity, kind Kind) error {
	if err := ref.Validate(); err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	if ref.Kind != kind {
		return fmt.Errorf("%s has incorrect reference kind", path)
	}
	if ref.Instance != r.Metadata.SourceIdentity.Instance {
		return fmt.Errorf("%s references a different source instance", path)
	}
	return nil
}

// Decode rejects unknown fields and extra JSON documents before validating.
func Decode(data []byte) (Resource, error) {
	var r Resource
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&r); err != nil {
		return Resource{}, errors.New("invalid resource JSON or unknown field")
	}
	if err := dec.Decode(new(any)); err != io.EOF {
		return Resource{}, errors.New("resource must contain exactly one JSON document")
	}
	// A Go bool cannot distinguish absent/null from explicit false. Enforce wire
	// presence before semantic validation so malformed snapshots never become
	// valid disabled resources silently.
	var envelope map[string]json.RawMessage
	if err := json.Unmarshal(data, &envelope); err != nil {
		return Resource{}, errors.New("resource must be an object")
	}
	if err := requireJSONFields(envelope, []string{"api_version", "kind", "metadata"}, nil); err != nil {
		return Resource{}, err
	}
	var metadata map[string]json.RawMessage
	if err := json.Unmarshal(envelope["metadata"], &metadata); err != nil {
		return Resource{}, errors.New("metadata must be an object")
	}
	if err := requireJSONFields(metadata, []string{"source_identity", "migration_job"}, nil); err != nil {
		return Resource{}, err
	}
	field := map[Kind]string{GroupKind: "group", ProviderKind: "provider", AggregateRouteKind: "aggregate_route", UserKind: "user", ApiKeyKind: "apikey"}[r.Kind]
	for name := range envelope {
		if name != "api_version" && name != "kind" && name != "metadata" && name != field {
			return Resource{}, errors.New("payload does not match resource kind")
		}
	}
	fields := map[Kind][]string{
		GroupKind:          {"name"},
		ProviderKind:       {"name", "type", "endpoint", "enabled", "api_keys", "model_mapping"},
		AggregateRouteKind: {"name", "members"},
		UserKind:           {"username", "role", "enabled", "group_ref"},
		ApiKeyKind:         {"name", "user_ref", "key", "enabled", "expires_at", "ip_allowlist", "provider_refs", "route_refs"},
	}[r.Kind]
	var payload map[string]json.RawMessage
	if field == "" || json.Unmarshal(envelope[field], &payload) != nil || payload == nil {
		return Resource{}, errors.New("resource payload is required")
	}
	if err := requireJSONFields(payload, fields, map[string]bool{"model_mapping": true, "expires_at": true, "ip_allowlist": true, "provider_refs": true, "route_refs": true}); err != nil {
		return Resource{}, err
	}
	if r.Kind == AggregateRouteKind {
		var members []map[string]json.RawMessage
		if err := json.Unmarshal(payload["members"], &members); err != nil {
			return Resource{}, errors.New("aggregate_route.members must be a list of objects")
		}
		for _, member := range members {
			if err := requireJSONFields(member, []string{"provider_ref", "priority", "weight"}, nil); err != nil {
				return Resource{}, err
			}
		}
	}
	if err := r.Validate(); err != nil {
		return Resource{}, err
	}
	return r, nil
}

func requireJSONFields(object map[string]json.RawMessage, fields []string, nullable map[string]bool) error {
	for _, field := range fields {
		value, ok := object[field]
		if !ok || (!nullable[field] && bytes.Equal(bytes.TrimSpace(value), []byte("null"))) {
			return fmt.Errorf("%s: explicit non-null field required", field)
		}
	}
	return nil
}

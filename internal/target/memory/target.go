// Package memory implements a deterministic, fault-injectable target simulator.
// It stores only logical fingerprints, never actual credentials.
package memory

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"sync"

	"github.com/hyperbolic-c/newapi2ACG/internal/model"
	"github.com/hyperbolic-c/newapi2ACG/internal/target"
)

type Fault string

const (
	FailBefore        Fault = "fail_before"
	TimeoutBefore     Fault = "timeout_before"
	LoseResponseAfter Fault = "lose_response_after"
)

type record struct {
	id       string
	resource model.Resource
}
type receipt struct{ signature, targetID string }
type Target struct {
	mu          sync.Mutex
	resources   map[model.Identity]record
	receipts    map[string]receipt
	intents     map[string]string
	contentKeys map[string]string
	faults      map[string]Fault
	sequence    uint64
}

func New() *Target {
	return &Target{resources: map[model.Identity]record{}, receipts: map[string]receipt{}, intents: map[string]string{}, contentKeys: map[string]string{}, faults: map[string]Fault{}}
}
func (t *Target) Capabilities(ctx context.Context) (target.Capabilities, error) {
	if err := ctx.Err(); err != nil {
		return target.Capabilities{}, err
	}
	return target.Capabilities{SourceIdentity: true, Idempotency: true, AtomicKeyGrants: true, OriginalKeys: true}, nil
}
func (t *Target) Inject(opID string, fault Fault) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.faults[opID] = fault
}
func (t *Target) Apply(ctx context.Context, changes []model.Change) ([]model.Result, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	seen := make(map[string]bool, len(changes))
	for _, change := range changes {
		if change.OpID == "" || seen[change.OpID] {
			return nil, errors.New("batch requires unique nonempty operation IDs")
		}
		seen[change.OpID] = true
	}
	results := make([]model.Result, 0, len(changes))
	for _, change := range changes {
		if err := ctx.Err(); err != nil {
			return results, err
		}
		fault := t.faults[change.OpID]
		delete(t.faults, change.OpID)
		if fault == FailBefore || fault == TimeoutBefore {
			outcome := model.Failed
			if fault == TimeoutBefore {
				outcome = model.Retryable
			}
			results = append(results, model.Result{OpID: change.OpID, Outcome: outcome, Err: errors.New("injected target failure")})
			continue
		}
		result := t.apply(change)
		if fault == LoseResponseAfter && result.Outcome == model.Succeeded {
			result.Outcome = model.Unknown
			result.Err = errors.New("target response lost")
		}
		results = append(results, result)
	}
	return results, nil
}
func (t *Target) apply(change model.Change) model.Result {
	result := model.Result{OpID: change.OpID, Outcome: model.Failed}
	fail := func(message string) model.Result { result.Err = errors.New(message); return result }
	if change.OpID == "" || change.IdemKey == "" {
		return fail("operation ID and idempotency key are required")
	}
	resource, err := model.Canonicalize(change.Resource)
	if err != nil {
		return fail("invalid logical resource")
	}
	if change.Op == model.Disable {
		switch resource.Kind {
		case model.ProviderKind:
			resource.Provider.Enabled = false
		case model.UserKind:
			resource.User.Enabled = false
		case model.ApiKeyKind:
			resource.ApiKey.Enabled = false
		default:
			return fail("resource cannot be disabled")
		}
	}
	hash, err := model.Hash(resource)
	if err != nil {
		return fail("cannot hash resource")
	}
	signature := string(change.Op) + ":" + hash + ":" + change.IdemKey
	if previous, ok := t.intents[change.OpID]; ok && previous != signature {
		return fail("operation ID reused for different intent")
	}
	if previous, ok := t.contentKeys[change.IdemKey]; ok && previous != hash {
		return fail("idempotency key reused for different content")
	}
	t.intents[change.OpID] = signature
	t.contentKeys[change.IdemKey] = hash
	if previous, ok := t.receipts[change.OpID]; ok {
		if previous.signature != signature {
			return fail("idempotency key reused for different operation")
		}
		return model.Result{OpID: change.OpID, Outcome: model.Succeeded, TargetID: previous.targetID}
	}
	identity := resource.Metadata.SourceIdentity
	existing, exists := t.resources[identity]
	switch change.Op {
	case model.Create:
		if exists {
			previousHash, _ := model.Hash(existing.resource)
			if previousHash != hash {
				return fail("source identity already has different content")
			}
		}
	case model.Update, model.Disable, model.Grant, model.Revoke:
		if !exists {
			return fail("resource not found")
		}
	case model.Delete:
		if exists {
			for otherID, other := range t.resources {
				if otherID != identity && slices.Contains(references(other.resource), identity) {
					return fail("resource is still referenced")
				}
			}
		}
		delete(t.resources, identity)
		t.receipts[change.OpID] = receipt{signature, existing.id}
		return model.Result{OpID: change.OpID, Outcome: model.Succeeded, TargetID: existing.id}
	default:
		return fail("unsupported target operation")
	}
	if change.Op == model.Grant || change.Op == model.Revoke {
		if resource.Kind != model.ApiKeyKind {
			return fail("grants only apply to API keys")
		}
		before := existing.resource.ApiKey
		after := resource.ApiKey
		if change.Op == model.Grant && (!subset(before.ProviderRefs, after.ProviderRefs) || !subset(before.RouteRefs, after.RouteRefs)) {
			return fail("grant operation cannot revoke access")
		}
		if change.Op == model.Revoke && (!subset(after.ProviderRefs, before.ProviderRefs) || !subset(after.RouteRefs, before.RouteRefs)) {
			return fail("revoke operation cannot grant access")
		}
		check, _ := model.Canonicalize(resource)
		check.ApiKey.ProviderRefs = before.ProviderRefs
		check.ApiKey.RouteRefs = before.RouteRefs
		beforeHash, _ := model.Hash(existing.resource)
		otherHash, _ := model.Hash(check)
		if beforeHash != otherHash {
			return fail("grant operation cannot modify non-authorization fields")
		}
	}
	for otherID, other := range t.resources {
		if otherID == identity {
			continue
		}
		// API key names are labels, not business uniqueness keys. Their secret
		// fingerprints and source identities carry the uniqueness constraints.
		if resource.Kind != model.ApiKeyKind && other.resource.Kind == resource.Kind && resourceName(other.resource) == resourceName(resource) {
			return fail("target name already occupied")
		}
		if resource.Kind == model.ApiKeyKind && other.resource.Kind == model.ApiKeyKind && resource.ApiKey.Key.Fingerprint == other.resource.ApiKey.Key.Fingerprint {
			return fail("API key already occupied")
		}
	}
	for _, ref := range references(resource) {
		if _, ok := t.resources[ref]; !ok {
			return fail("referenced target resource not found")
		}
	}
	if !exists {
		t.sequence++
		existing.id = strconv.FormatUint(t.sequence, 10)
	}
	resource.Metadata.ContentHash = hash
	t.resources[identity] = record{existing.id, resource}
	t.receipts[change.OpID] = receipt{signature, existing.id}
	return model.Result{OpID: change.OpID, Outcome: model.Succeeded, TargetID: existing.id}
}
func subset(a, b []model.Identity) bool {
	for _, id := range a {
		if !slices.Contains(b, id) {
			return false
		}
	}
	return true
}
func references(r model.Resource) []model.Identity {
	refs := []model.Identity{}
	switch r.Kind {
	case model.UserKind:
		refs = append(refs, r.User.GroupRef)
	case model.AggregateRouteKind:
		for _, m := range r.AggregateRoute.Members {
			refs = append(refs, m.ProviderRef)
		}
	case model.ApiKeyKind:
		refs = append(refs, r.ApiKey.UserRef)
		refs = append(refs, r.ApiKey.ProviderRefs...)
		refs = append(refs, r.ApiKey.RouteRefs...)
	}
	return refs
}
func resourceName(r model.Resource) string {
	switch r.Kind {
	case model.GroupKind:
		return r.Group.Name
	case model.ProviderKind:
		return r.Provider.Name
	case model.AggregateRouteKind:
		return r.AggregateRoute.Name
	case model.UserKind:
		return r.User.Username
	case model.ApiKeyKind:
		return r.ApiKey.Name
	}
	return ""
}
func (t *Target) Observe(ctx context.Context, kinds []model.Kind) (*model.Observed, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	observed := &model.Observed{Items: []model.Observation{}}
	for _, entry := range t.resources {
		if len(kinds) > 0 && !slices.Contains(kinds, entry.resource.Kind) {
			continue
		}
		resource, err := model.Canonicalize(entry.resource)
		if err != nil {
			return nil, fmt.Errorf("invalid simulator state")
		}
		resource.Metadata.ContentHash = entry.resource.Metadata.ContentHash
		item := model.Observation{TargetID: entry.id, Resource: resource, Unreadable: []string{}}
		switch resource.Kind {
		case model.ProviderKind:
			resource.Provider.APIKeys = nil
			item.Unreadable = []string{"provider.api_keys"}
		case model.ApiKeyKind:
			resource.ApiKey.Key = model.SecretRef{}
			item.Unreadable = []string{"apikey.key"}
		}
		observed.Items = append(observed.Items, item)
	}
	slices.SortFunc(observed.Items, func(a, b model.Observation) int {
		if a.Resource.Metadata.SourceIdentity.String() < b.Resource.Metadata.SourceIdentity.String() {
			return -1
		}
		return 1
	})
	return observed, nil
}

// Clone is used by file targets to stage a batch before its atomic publication.
// Receipts and fault injections are independent in the returned simulator.
func (t *Target) Clone() (*Target, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	copy := New()
	copy.sequence = t.sequence
	for identity, entry := range t.resources {
		r, err := model.Canonicalize(entry.resource)
		if err != nil {
			return nil, err
		}
		r.Metadata.ContentHash = entry.resource.Metadata.ContentHash
		copy.resources[identity] = record{entry.id, r}
	}
	for key, value := range t.receipts {
		copy.receipts[key] = value
	}
	for key, value := range t.intents {
		copy.intents[key] = value
	}
	for key, value := range t.contentKeys {
		copy.contentKeys[key] = value
	}
	for key, value := range t.faults {
		copy.faults[key] = value
	}
	return copy, nil
}

// Snapshot returns complete logical records for report rendering. Unlike
// Observe it includes fingerprints, but still cannot contain raw credentials.
func (t *Target) Snapshot(ctx context.Context) ([]model.Resource, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	items := []model.Resource{}
	for _, entry := range t.resources {
		r, err := model.Canonicalize(entry.resource)
		if err != nil {
			return nil, err
		}
		r.Metadata.ContentHash = entry.resource.Metadata.ContentHash
		items = append(items, r)
	}
	slices.SortFunc(items, func(a, b model.Resource) int {
		if a.Metadata.SourceIdentity.String() < b.Metadata.SourceIdentity.String() {
			return -1
		}
		if a.Metadata.SourceIdentity == b.Metadata.SourceIdentity {
			return 0
		}
		return 1
	})
	return items, nil
}

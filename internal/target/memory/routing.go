package memory

import (
	"context"
	"errors"
	"math"
	"strings"
	"time"

	"github.com/hyperbolic-c/newapi2ACG/internal/model"
)

// Candidates returns authorized, enabled members of the highest eligible
// priority. Selection is a simulator of the logical ACG contract, not a claim
// to reproduce New API's DB/cache-specific retry and weight algorithms.
func (t *Target) Candidates(ctx context.Context, keyID model.Identity, name string) ([]model.RouteMember, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	entry, ok := t.resources[keyID]
	if !ok || entry.resource.ApiKey == nil {
		return nil, errors.New("API key not found")
	}
	key := entry.resource.ApiKey
	if !key.Enabled || (key.ExpiresAt != nil && !time.Now().Before(*key.ExpiresAt)) {
		return nil, errors.New("API key disabled or expired")
	}
	owner, ok := t.resources[key.UserRef]
	if !ok || owner.resource.User == nil || !owner.resource.User.Enabled {
		return nil, errors.New("user disabled or missing")
	}
	var members []model.RouteMember
	for _, entry := range t.resources {
		resource := entry.resource
		if resource.AggregateRoute != nil && resource.AggregateRoute.Name == name && subset([]model.Identity{resource.Metadata.SourceIdentity}, key.RouteRefs) {
			members = resource.AggregateRoute.Members
			break
		}
	}
	// Provider identities in route_refs explicitly authorize a direct route;
	// having the provider in provider_refs alone never grants direct access.
	if len(members) == 0 {
		for _, entry := range t.resources {
			p := entry.resource.Provider
			if p == nil {
				continue
			}
			if strings.HasPrefix(name, p.Name+"/") && len(name) > len(p.Name)+1 && subset([]model.Identity{entry.resource.Metadata.SourceIdentity}, key.RouteRefs) {
				members = []model.RouteMember{{ProviderRef: entry.resource.Metadata.SourceIdentity, Weight: 1}}
				break
			}
		}
	}
	eligible := []model.RouteMember{}
	highest := int64(math.MinInt64)
	for _, member := range members {
		if !subset([]model.Identity{member.ProviderRef}, key.ProviderRefs) {
			continue
		}
		provider, ok := t.resources[member.ProviderRef]
		if !ok || provider.resource.Provider == nil || !provider.resource.Provider.Enabled {
			continue
		}
		if member.Priority > highest {
			eligible = eligible[:0]
			highest = member.Priority
		}
		if member.Priority == highest {
			eligible = append(eligible, member)
		}
	}
	if len(eligible) == 0 {
		return nil, errors.New("no authorized enabled route members")
	}
	return eligible, nil
}

// Select maps an externally supplied random ticket to an eligible member.
// Keeping randomness outside the simulator makes weighted boundary tests exact.
func (t *Target) Select(ctx context.Context, key model.Identity, modelName string, ticket uint64) (model.Identity, error) {
	members, err := t.Candidates(ctx, key, modelName)
	if err != nil {
		return model.Identity{}, err
	}
	var total uint64
	for _, member := range members {
		weight := uint64(member.Weight)
		if math.MaxUint64-total < weight {
			return model.Identity{}, errors.New("route weight total overflows")
		}
		total += weight
	}
	if total == 0 {
		return members[ticket%uint64(len(members))].ProviderRef, nil
	}
	remaining := ticket % total
	for _, member := range members {
		weight := uint64(member.Weight)
		if remaining < weight {
			return member.ProviderRef, nil
		}
		remaining -= weight
	}
	return model.Identity{}, errors.New("no selectable route member")
}

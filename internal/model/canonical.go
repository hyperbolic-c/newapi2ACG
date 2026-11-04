package model

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"slices"
)

// Canonicalize returns managed content only. It never mutates the input and
// deliberately removes create-only user initialization and the previous hash.
func Canonicalize(resource Resource) (Resource, error) {
	if err := resource.Validate(); err != nil {
		return Resource{}, err
	}
	data, err := json.Marshal(resource)
	if err != nil {
		return Resource{}, err
	}
	var r Resource
	if err := json.Unmarshal(data, &r); err != nil {
		return Resource{}, err
	}
	r.Metadata.ContentHash = ""
	switch r.Kind {
	case ProviderKind:
		slices.SortFunc(r.Provider.APIKeys, func(a, b SecretRef) int {
			if a.Fingerprint < b.Fingerprint {
				return -1
			}
			if a.Fingerprint > b.Fingerprint {
				return 1
			}
			return 0
		})
		r.Provider.APIKeys = slices.Compact(r.Provider.APIKeys)
		if r.Provider.ModelMapping == nil {
			r.Provider.ModelMapping = map[string]string{}
		}
	case AggregateRouteKind:
		slices.SortFunc(r.AggregateRoute.Members, func(a, b RouteMember) int { return compareIdentity(a.ProviderRef, b.ProviderRef) })
		r.AggregateRoute.Members = slices.Compact(r.AggregateRoute.Members)
	case ApiKeyKind:
		r.ApiKey.ProviderRefs = canonicalRefs(r.ApiKey.ProviderRefs)
		r.ApiKey.RouteRefs = canonicalRefs(r.ApiKey.RouteRefs)
		if r.ApiKey.IPAllowlist == nil {
			r.ApiKey.IPAllowlist = []string{}
		}
		slices.Sort(r.ApiKey.IPAllowlist)
		r.ApiKey.IPAllowlist = slices.Compact(r.ApiKey.IPAllowlist)
		if r.ApiKey.ExpiresAt != nil {
			utc := r.ApiKey.ExpiresAt.UTC()
			r.ApiKey.ExpiresAt = &utc
		}
	}
	return r, nil
}

func canonicalRefs(refs []Identity) []Identity {
	if refs == nil {
		return []Identity{}
	}
	slices.SortFunc(refs, compareIdentity)
	return slices.Compact(refs)
}
func compareIdentity(a, b Identity) int {
	if a.String() < b.String() {
		return -1
	}
	if a.String() > b.String() {
		return 1
	}
	return 0
}

func Hash(resource Resource) (string, error) {
	r, err := Canonicalize(resource)
	if err != nil {
		return "", err
	}
	data, err := json.Marshal(r)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:]), nil
}

// Package report renders deterministic, redacted dry-run artifacts.
package report

import (
	"encoding/json"
	"slices"

	"github.com/hyperbolic-c/newapi2ACG/internal/model"
	"github.com/hyperbolic-c/newapi2ACG/internal/secrets"
)

type Operation struct {
	OpID     string        `json:"op_id"`
	Identity string        `json:"source_identity"`
	Op       model.Op      `json:"operation"`
	Outcome  model.Outcome `json:"outcome"`
}

// Render is a report projection, not an importable acg/v1 snapshot: fingerprint
// prefixes cannot be used to recover credentials or confirm secret equality.
func Render(resources []model.Resource, operations []Operation) ([]byte, error) {
	items := make([]model.Resource, 0, len(resources))
	for _, resource := range resources {
		copy, err := model.Canonicalize(resource)
		if err != nil {
			return nil, err
		}
		copy.Metadata.ContentHash = resource.Metadata.ContentHash
		switch copy.Kind {
		case model.ProviderKind:
			for i := range copy.Provider.APIKeys {
				copy.Provider.APIKeys[i].Fingerprint = secrets.ShortFingerprint(copy.Provider.APIKeys[i].Fingerprint)
			}
		case model.ApiKeyKind:
			copy.ApiKey.Key.Fingerprint = secrets.ShortFingerprint(copy.ApiKey.Key.Fingerprint)
		}
		items = append(items, copy)
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
	ops := append([]Operation{}, operations...)
	slices.SortFunc(ops, func(a, b Operation) int {
		if a.OpID < b.OpID {
			return -1
		}
		if a.OpID == b.OpID {
			return 0
		}
		return 1
	})
	document := struct {
		Version    string           `json:"report_version"`
		Resources  []model.Resource `json:"resources"`
		Operations []Operation      `json:"operations"`
	}{"migrator-report/v1", items, ops}
	return json.MarshalIndent(document, "", "  ")
}

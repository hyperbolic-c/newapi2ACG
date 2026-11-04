// Package state defines the short-transaction persistence boundary. Source
// adapters must strip secret columns before constructing mirror changes.
package state

import (
	"github.com/hyperbolic-c/newapi2ACG/internal/model"
)

type RowChange struct {
	Dataset            string
	Key                string
	Delete             bool
	Data               map[string]any
	SecretFingerprints map[string]string
}
type MirrorRow struct {
	Dataset, Key       string
	Data               map[string]any
	SecretFingerprints map[string]string
}
type PendingItem struct {
	Identity model.Identity
	Version  int64
	Reason   string
}

// AppliedResult carries the immutable intent so a fresh transaction can verify
// it against operations before writing successful resource receipts. Never put
// raw credentials or driver error messages in these fields.
type AppliedResult struct {
	Change     model.Change
	Result     model.Result
	ConfigHash string
}

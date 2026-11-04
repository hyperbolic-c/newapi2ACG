// Package state defines the short-transaction persistence boundary. The
// service stores only sync state (binlog position, queue, write log, last
// written content); it never stores a copy of source data (design v3.3).
package state

import (
	"github.com/hyperbolic-c/newapi2ACG/internal/model"
)

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

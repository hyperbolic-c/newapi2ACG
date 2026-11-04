package state

import (
	"context"
	"github.com/hyperbolic-c/newapi2ACG/internal/model"
)

// Store binds each transaction to a job and target. Naming allocation and the
// failure queue arrive with the writer; they are not silent no-op methods here.
type Store interface {
	Begin(context.Context, string, string) (Transaction, error)
}
type Transaction interface {
	// SetFullSyncStart records G0 before a full read and resets the
	// incremental position to it: binlog consumption starts from G0 (design 9.4).
	SetFullSyncStart(g0 string) error
	// Advance records the GTID set incremental sync has processed.
	Advance(pos string) error
	// Position returns the processed GTID set; "" when none is recorded.
	Position() (string, error)
	Enqueue([]model.Identity, string) error
	TakePending(int) ([]PendingItem, error)
	Dequeue([]PendingItem) error
	RecordIntent([]model.Change) error
	RecordResults([]AppliedResult) error
	LastApplied([]model.Identity) (map[model.Identity]model.Resource, error)
	Commit() error
	Rollback() error
}

package state

import (
	"context"
	"github.com/hyperbolic-c/newapi2ACG/internal/model"
)

// Store binds each transaction to a job and target. Mapping, naming allocation
// and dependency maintenance are separate milestones, not silent no-op methods.
type Store interface {
	Begin(context.Context, string, string) (Transaction, error)
}
type Transaction interface {
	ApplyMirror(int64, []RowChange) error
	ReadMirror(int64, string, string) (*MirrorRow, error)
	Advance(int64, string) error
	Position(int64) (string, error)
	Enqueue([]model.Identity, string) error
	TakePending(int) ([]PendingItem, error)
	Dequeue([]PendingItem) error
	RecordIntent([]model.Change) error
	RecordResults([]AppliedResult) error
	LastApplied([]model.Identity) (map[model.Identity]model.Resource, error)
	Commit() error
	Rollback() error
}

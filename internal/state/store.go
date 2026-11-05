// Package state persists the minimum needed to run statelessly (design-core
// §8): task phase and lease, the checkpoint, and the source→target resource
// map. It never stores a copy of source data.
package state

import (
	"context"
	"errors"
	"time"

	"github.com/hyperbolic-c/newapi2ACG/internal/event"
	"github.com/hyperbolic-c/newapi2ACG/internal/target"
)

type Phase string

const (
	Initializing Phase = "initializing"
	Snapshotting Phase = "snapshotting"
	CatchingUp   Phase = "catching_up"
	Streaming    Phase = "streaming"
	Reconciling  Phase = "reconciling"
	Paused       Phase = "paused"
	Failed       Phase = "failed"
)

func (p Phase) Valid() bool {
	switch p {
	case Initializing, Snapshotting, CatchingUp, Streaming, Reconciling, Paused, Failed:
		return true
	}
	return false
}

// ErrLeaseLost means another instance owns the task or the lease expired.
// Writes are fenced on the lease, so a stale instance cannot commit.
var ErrLeaseLost = errors.New("task lease lost")

// ErrTaskNotFound means the task has not been registered.
var ErrTaskNotFound = errors.New("task not registered")

type Task struct {
	ID         string
	SourceID   string
	Phase      Phase
	ConfigHash string
	LastError  string
	LeaseOwner string
	LeaseUntil time.Time
	UpdatedAt  time.Time
}

type Checkpoint struct {
	SnapshotPosition event.SourcePosition
	AppliedPosition  event.SourcePosition
	SnapshotDone     bool
	ReconcileRunID   string
}

// Lease identifies the holder of a task's execution right.
type Lease struct {
	TaskID string
	Owner  string
}

// MapKey identifies one source→target association.
type MapKey struct {
	Source   target.SourceRef
	Identity target.TargetIdentity
}

// MapEntry is one row of the resource map. Hash is an HMAC of the applied
// target data (never a plain hash of secrets, design-core §8.3).
type MapEntry struct {
	MapKey
	TargetID    string // "" in a Commit keeps the stored value
	Hash        string
	LastSeenRun string // "" in a Commit keeps the stored value
}

// Commit is written atomically and fenced by the lease.
type Commit struct {
	Upserts    []MapEntry
	Deletes    []MapKey
	Checkpoint *Checkpoint // nil leaves the checkpoint unchanged
}

func (c Commit) Empty() bool {
	return len(c.Upserts) == 0 && len(c.Deletes) == 0 && c.Checkpoint == nil
}

type Store interface {
	// EnsureTask registers the task in Initializing if absent; an existing
	// task is returned unchanged.
	EnsureTask(ctx context.Context, taskID, sourceID, configHash string) (Task, error)
	LoadTask(ctx context.Context, taskID string) (Task, error)

	// AcquireLease succeeds when the task is unleased, the lease expired,
	// or owner already holds it. RenewLease fails with ErrLeaseLost when
	// owner no longer holds it.
	AcquireLease(ctx context.Context, taskID, owner string, ttl time.Duration) (bool, error)
	RenewLease(ctx context.Context, l Lease, ttl time.Duration) error
	ReleaseLease(ctx context.Context, l Lease) error

	// SetPhase and SetConfigHash are fenced by the lease.
	SetPhase(ctx context.Context, l Lease, phase Phase, lastError string) error
	SetConfigHash(ctx context.Context, l Lease, configHash string) error
	// SetPhaseAdmin is for operator commands (pause, resume, recover) and
	// does not require the lease; the running instance observes the change.
	SetPhaseAdmin(ctx context.Context, taskID string, phase Phase, lastError string) error

	LoadCheckpoint(ctx context.Context, taskID string) (Checkpoint, error)
	Commit(ctx context.Context, l Lease, c Commit) error

	// Hashes returns stored hashes by target identity.
	Hashes(ctx context.Context, taskID string, ids []target.TargetIdentity) (map[target.TargetIdentity]string, error)
	// Unseen lists entries of the given datasets whose last_seen_run is not runID.
	Unseen(ctx context.Context, taskID, runID string, datasets []string) ([]MapEntry, error)
}

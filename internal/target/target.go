// Package target defines how the pipeline writes to a destination (ACG or a
// mock). Business fields are opaque here; mapping decides their content.
package target

import (
	"context"
	"errors"
	"fmt"
)

type Action string

const (
	ActionUpsert Action = "upsert"
	ActionDelete Action = "delete"
)

// TargetIdentity is the stable logical identity a mapper assigns to a target
// resource (for example kind "provider", id "channel:12"). It is not the
// target's internal ID, which is returned by Apply.
type TargetIdentity struct {
	Kind string `json:"kind"`
	ID   string `json:"id"`
}

func (t TargetIdentity) String() string { return t.Kind + "/" + t.ID }

// SourceRef is the source row an operation was derived from.
type SourceRef struct {
	Dataset string `json:"dataset"`
	Key     string `json:"key"`
}

// TargetOperation is one desired change. Apply must be idempotent per
// Identity: applying the same upsert twice leaves one resource.
type TargetOperation struct {
	Identity TargetIdentity
	Action   Action
	Source   SourceRef
	Data     map[string]any
}

// TargetResource is the target's current state of a managed resource. Data
// must contain the managed fields in the same shape the mapper produces so
// reconciliation can compare them.
type TargetResource struct {
	Identity TargetIdentity
	TargetID string
	Data     map[string]any
}

// ApplyResult reports the target ID of an applied upsert.
type ApplyResult struct {
	Identity TargetIdentity
	TargetID string
}

// Target writes operations and inspects current state. Inspect serves both
// reconciliation and confirming whether a failed or timed-out Apply actually
// took effect (design-core §9.2). Missing resources are simply absent from
// Inspect's result.
type Target interface {
	Apply(ctx context.Context, ops []TargetOperation) ([]ApplyResult, error)
	Inspect(ctx context.Context, ids []TargetIdentity) ([]TargetResource, error)
}

// PermanentError marks a failure that retrying cannot fix (validation,
// conflict with a resource the task does not own, mapping errors).
type PermanentError struct{ Err error }

func (e *PermanentError) Error() string { return "permanent: " + e.Err.Error() }
func (e *PermanentError) Unwrap() error { return e.Err }

// Permanent wraps err as a PermanentError.
func Permanent(format string, args ...any) error {
	return &PermanentError{Err: fmt.Errorf(format, args...)}
}

func IsPermanent(err error) bool {
	var p *PermanentError
	return errors.As(err, &p)
}

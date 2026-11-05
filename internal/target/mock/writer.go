// Package mock is an in-memory Target for tests and dry runs before the ACG
// API is available. It supports fault injection for reliability tests.
package mock

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"sync"

	"github.com/hyperbolic-c/newapi2ACG/internal/target"
)

type Fault int

const (
	// FaultFail returns a transient error without applying anything.
	FaultFail Fault = iota + 1
	// FaultLostResponse applies everything, then returns a transient error,
	// as if the response was lost after the target committed.
	FaultLostResponse
	// FaultPermanent returns a permanent error without applying anything.
	FaultPermanent
)

// ErrTransient is returned for injected transient faults.
var ErrTransient = errors.New("mock target: transient failure")

type Writer struct {
	mu        sync.Mutex
	resources map[target.TargetIdentity]target.TargetResource
	nextID    int
	faults    []Fault
	applied   int
	calls     int
}

func New() *Writer {
	return &Writer{resources: map[target.TargetIdentity]target.TargetResource{}}
}

// InjectFaults queues faults; each Apply call consumes one.
func (w *Writer) InjectFaults(f ...Fault) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.faults = append(w.faults, f...)
}

func (w *Writer) Apply(ctx context.Context, ops []target.TargetOperation) ([]target.ApplyResult, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	w.calls++
	var fault Fault
	if len(w.faults) > 0 {
		fault, w.faults = w.faults[0], w.faults[1:]
	}
	switch fault {
	case FaultFail:
		return nil, ErrTransient
	case FaultPermanent:
		return nil, target.Permanent("mock target rejected the operations")
	}
	results := make([]target.ApplyResult, 0, len(ops))
	for _, op := range ops {
		switch op.Action {
		case target.ActionUpsert:
			res, ok := w.resources[op.Identity]
			if !ok {
				w.nextID++
				res = target.TargetResource{Identity: op.Identity, TargetID: fmt.Sprintf("mock-%d", w.nextID)}
			}
			res.Data = maps.Clone(op.Data)
			w.resources[op.Identity] = res
			results = append(results, target.ApplyResult{Identity: op.Identity, TargetID: res.TargetID})
		case target.ActionDelete:
			delete(w.resources, op.Identity)
		default:
			return results, target.Permanent("unknown action %q", op.Action)
		}
		w.applied++
	}
	if fault == FaultLostResponse {
		return nil, ErrTransient
	}
	return results, nil
}

func (w *Writer) Inspect(ctx context.Context, ids []target.TargetIdentity) ([]target.TargetResource, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	var out []target.TargetResource
	for _, id := range ids {
		if res, ok := w.resources[id]; ok {
			res.Data = maps.Clone(res.Data)
			out = append(out, res)
		}
	}
	return out, nil
}

// Set overwrites a resource directly, simulating a manual change in ACG.
func (w *Writer) Set(id target.TargetIdentity, data map[string]any) {
	w.mu.Lock()
	defer w.mu.Unlock()
	res, ok := w.resources[id]
	if !ok {
		w.nextID++
		res = target.TargetResource{Identity: id, TargetID: fmt.Sprintf("mock-%d", w.nextID)}
	}
	res.Data = maps.Clone(data)
	w.resources[id] = res
}

// Resources returns a copy of all resources.
func (w *Writer) Resources() map[target.TargetIdentity]map[string]any {
	w.mu.Lock()
	defer w.mu.Unlock()
	out := make(map[target.TargetIdentity]map[string]any, len(w.resources))
	for id, res := range w.resources {
		out[id] = maps.Clone(res.Data)
	}
	return out
}

// AppliedOperations counts operations applied (including ones whose response
// was "lost"); Calls counts Apply invocations.
func (w *Writer) AppliedOperations() int {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.applied
}

func (w *Writer) Calls() int {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.calls
}

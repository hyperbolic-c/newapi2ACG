// Package file publishes redacted simulation reports. It is a dry-run target,
// not a persistent ACG replacement: a new process rebuilds its simulation from
// desired state, never imports truncated fingerprints from report artifacts.
package file

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sync"

	"github.com/hyperbolic-c/newapi2ACG/internal/model"
	"github.com/hyperbolic-c/newapi2ACG/internal/report"
	"github.com/hyperbolic-c/newapi2ACG/internal/target"
	"github.com/hyperbolic-c/newapi2ACG/internal/target/memory"
)

type Target struct {
	mu         sync.Mutex
	state      *memory.Target
	path       string
	operations map[string]report.Operation
	publish    func(context.Context, string, io.Reader) error
}

func New(path string) *Target {
	return &Target{state: memory.New(), path: path, operations: map[string]report.Operation{}, publish: writeAtomic}
}
func (t *Target) Capabilities(ctx context.Context) (target.Capabilities, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.state.Capabilities(ctx)
}
func (t *Target) Observe(ctx context.Context, kinds []model.Kind) (*model.Observed, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.state.Observe(ctx, kinds)
}
func (t *Target) Apply(ctx context.Context, changes []model.Change) ([]model.Result, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	staged, err := t.state.Clone()
	if err != nil {
		return nil, err
	}
	results, applyErr := staged.Apply(ctx, changes)
	if applyErr != nil && len(results) == 0 {
		return nil, applyErr
	}
	resources, err := staged.Snapshot(ctx)
	if err != nil {
		return publicationFailed(results), err
	}
	operations := make(map[string]report.Operation, len(t.operations)+len(results))
	for id, op := range t.operations {
		operations[id] = op
	}
	inputs := map[string]model.Change{}
	for _, change := range changes {
		inputs[change.OpID] = change
	}
	for _, result := range results {
		change := inputs[result.OpID]
		if _, exists := operations[result.OpID]; exists && result.Outcome == model.Failed {
			// A rejected reuse of an operation ID must not overwrite its history.
			continue
		}
		operations[result.OpID] = report.Operation{OpID: result.OpID, Identity: change.Resource.Metadata.SourceIdentity.String(), Op: change.Op, Outcome: result.Outcome}
	}
	ops := make([]report.Operation, 0, len(operations))
	for _, op := range operations {
		ops = append(ops, op)
	}
	data, err := report.Render(resources, ops)
	if err != nil {
		return publicationFailed(results), err
	}
	if err := t.publish(ctx, t.path, bytes.NewReader(data)); err != nil {
		return publicationFailed(results), err
	}
	t.state = staged
	t.operations = operations
	return results, applyErr
}
func publicationFailed(results []model.Result) []model.Result {
	for i := range results {
		if results[i].Outcome == model.Succeeded || results[i].Outcome == model.Unknown {
			results[i].Outcome = model.Retryable
			results[i].TargetID = ""
			results[i].Err = errors.New("report publication failed; simulation not committed")
		}
	}
	return results
}

// writeAtomic guarantees that a failed pre-rename write leaves the previous
// artifact intact. It promises atomic replacement, not power-loss durability.
func writeAtomic(ctx context.Context, path string, input io.Reader) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if path == "" {
		return errors.New("report path is required")
	}
	directory := filepath.Dir(path)
	if err := os.MkdirAll(directory, 0700); err != nil {
		return err
	}
	temp, err := os.CreateTemp(directory, ".migrator-report-*")
	if err != nil {
		return err
	}
	name := temp.Name()
	defer func() { _ = os.Remove(name) }() // temp file; absent after a successful rename
	if _, err := io.Copy(temp, input); err != nil {
		_ = temp.Close()
		return err
	}
	if err := temp.Sync(); err != nil {
		_ = temp.Close()
		return err
	}
	if err := temp.Close(); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return os.Rename(name, path)
}

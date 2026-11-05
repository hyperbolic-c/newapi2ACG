package sync

import (
	"context"
	"encoding/json"
	"log/slog"

	"github.com/hyperbolic-c/newapi2ACG/internal/event"
	"github.com/hyperbolic-c/newapi2ACG/internal/mapping"
	"github.com/hyperbolic-c/newapi2ACG/internal/observability"
	"github.com/hyperbolic-c/newapi2ACG/internal/secrets"
	"github.com/hyperbolic-c/newapi2ACG/internal/state"
	"github.com/hyperbolic-c/newapi2ACG/internal/target"
)

// Pipeline is the single processing path for every collector (design-core
// §2.1): map → decide what must change → write → commit resource map and
// checkpoint together. Reliability model (§8.4): at-least-once processing,
// idempotent target writes, and state committed only after the target
// confirmed the writes.
type Pipeline struct {
	TaskID  string
	Mapper  mapping.Mapper
	Target  target.Target
	Store   state.Store
	Hasher  *secrets.Fingerprinter
	Chunk   int // max operations per Target.Apply call
	Retry   Retry
	Log     *slog.Logger
	Metrics *observability.Metrics
}

type compareMode int

const (
	// compareStored skips upserts whose hash equals the last applied hash
	// (full and incremental sync).
	compareStored compareMode = iota
	// compareTarget compares with the target's current state via Inspect
	// (reconciliation: catches manual changes and failed writes).
	compareTarget
)

type batchOptions struct {
	mode       compareMode
	runID      string            // marks entries seen in a snapshot pass
	checkpoint *state.Checkpoint // committed together with the batch
}

// process maps a batch and applies it. Mapping errors are permanent.
func (p *Pipeline) process(ctx context.Context, lease state.Lease, batch event.ChangeBatch, opt batchOptions) error {
	ops, err := p.Mapper.Transform(ctx, batch)
	if err != nil {
		return target.Permanent("mapping failed: %v", err)
	}
	if err := p.applyOps(ctx, lease, ops, opt); err != nil {
		return err
	}
	p.Metrics.BatchesApplied.Add(1)
	p.Metrics.EventsApplied.Add(int64(len(batch.Events)))
	return nil
}

type planned struct {
	op   target.TargetOperation
	hash string
	send bool
}

func (p *Pipeline) applyOps(ctx context.Context, lease state.Lease, ops []target.TargetOperation, opt batchOptions) error {
	plan, err := p.plan(ctx, ops, opt.mode)
	if err != nil {
		return err
	}
	var send []target.TargetOperation
	for _, pl := range plan {
		if pl.send {
			send = append(send, pl.op)
		}
	}
	targetIDs := map[target.TargetIdentity]string{}
	for start := 0; start < len(send); start += p.chunk() {
		chunk := send[start:min(start+p.chunk(), len(send))]
		ids, err := p.applyChunk(ctx, chunk)
		if err != nil {
			return err
		}
		for k, v := range ids {
			targetIDs[k] = v
		}
	}
	p.Metrics.OperationsSent.Add(int64(len(send)))
	p.Metrics.OperationsSkipped.Add(int64(len(plan) - len(send)))
	if opt.mode == compareTarget {
		p.Metrics.ReconcileFixes.Add(int64(len(send)))
	}
	commit := p.commitFor(plan, targetIDs, opt)
	if commit.Empty() {
		return nil
	}
	if err := p.Retry.Do(ctx, "commit sync state", func() error { return p.Store.Commit(ctx, lease, commit) }); err != nil {
		return err
	}
	if commit.Checkpoint != nil {
		p.Metrics.SetPosition(commit.Checkpoint.AppliedPosition.String())
	}
	return nil
}

func (p *Pipeline) chunk() int {
	if p.Chunk <= 0 {
		return 100
	}
	return p.Chunk
}

// plan decides which operations to send. Ownership and hashes evolve in
// batch order, so an insert followed by a delete in one transaction ends
// with the resource deleted.
func (p *Pipeline) plan(ctx context.Context, ops []target.TargetOperation, mode compareMode) ([]planned, error) {
	ids := make([]target.TargetIdentity, 0, len(ops))
	for _, op := range ops {
		ids = append(ids, op.Identity)
	}
	var stored map[target.TargetIdentity]string
	if err := p.Retry.Do(ctx, "read resource map", func() (err error) {
		stored, err = p.Store.Hashes(ctx, p.TaskID, ids)
		return err
	}); err != nil {
		return nil, err
	}
	current := map[target.TargetIdentity]string{} // target-side hash (reconcile only)
	present := map[target.TargetIdentity]bool{}
	if mode == compareTarget {
		var resources []target.TargetResource
		if err := p.Retry.Do(ctx, "inspect target", func() (err error) {
			resources, err = p.Target.Inspect(ctx, ids)
			return err
		}); err != nil {
			return nil, err
		}
		for _, r := range resources {
			h, err := p.hash(r.Data)
			if err != nil {
				return nil, err
			}
			current[r.Identity], present[r.Identity] = h, true
		}
	}
	out := make([]planned, 0, len(ops))
	for _, op := range ops {
		pl := planned{op: op}
		_, owned := stored[op.Identity]
		switch op.Action {
		case target.ActionUpsert:
			h, err := p.hash(op.Data)
			if err != nil {
				return nil, err
			}
			pl.hash = h
			if mode == compareTarget {
				pl.send = !present[op.Identity] || current[op.Identity] != h
				current[op.Identity], present[op.Identity] = h, true
			} else {
				pl.send = stored[op.Identity] != h || !owned
			}
			stored[op.Identity] = h
		case target.ActionDelete:
			// Only resources this task created are deleted (design-core §6.4).
			pl.send = owned && (mode != compareTarget || present[op.Identity])
			delete(stored, op.Identity)
			delete(present, op.Identity)
		default:
			return nil, target.Permanent("unknown action %q", op.Action)
		}
		out = append(out, pl)
	}
	return out, nil
}

// applyChunk applies operations with retries. When Apply fails with an
// unclear outcome, Inspect decides whether the writes already took effect,
// so a lost response does not cause a blind repeat (design-core §8.4).
func (p *Pipeline) applyChunk(ctx context.Context, ops []target.TargetOperation) (map[target.TargetIdentity]string, error) {
	ids := map[target.TargetIdentity]string{}
	err := p.Retry.Do(ctx, "apply to target", func() error {
		results, err := p.Target.Apply(ctx, ops)
		if err == nil {
			for _, r := range results {
				ids[r.Identity] = r.TargetID
			}
			return nil
		}
		if target.IsPermanent(err) || ctx.Err() != nil {
			return err
		}
		confirmed, verr := p.verify(ctx, ops)
		if verr == nil && confirmed != nil {
			p.Log.Info("target write confirmed after an unclear failure", "operations", len(ops))
			for k, v := range confirmed {
				ids[k] = v
			}
			return nil
		}
		return err
	})
	return ids, err
}

// verify reports the target IDs if the target already reflects every
// operation's final effect, or nil if it does not.
func (p *Pipeline) verify(ctx context.Context, ops []target.TargetOperation) (map[target.TargetIdentity]string, error) {
	want := map[target.TargetIdentity]*string{} // nil = must be absent
	var ids []target.TargetIdentity
	for _, op := range ops {
		if _, seen := want[op.Identity]; !seen {
			ids = append(ids, op.Identity)
		}
		if op.Action == target.ActionDelete {
			want[op.Identity] = nil
			continue
		}
		h, err := p.hash(op.Data)
		if err != nil {
			return nil, err
		}
		want[op.Identity] = &h
	}
	resources, err := p.Target.Inspect(ctx, ids)
	if err != nil {
		return nil, err
	}
	have := map[target.TargetIdentity]target.TargetResource{}
	for _, r := range resources {
		have[r.Identity] = r
	}
	out := map[target.TargetIdentity]string{}
	for id, h := range want {
		r, ok := have[id]
		if h == nil {
			if ok {
				return nil, nil
			}
			continue
		}
		if !ok {
			return nil, nil
		}
		got, err := p.hash(r.Data)
		if err != nil || got != *h {
			return nil, err
		}
		out[id] = r.TargetID
	}
	return out, nil
}

func (p *Pipeline) commitFor(plan []planned, targetIDs map[target.TargetIdentity]string, opt batchOptions) state.Commit {
	upserts := map[state.MapKey]state.MapEntry{}
	var order []state.MapKey
	var deletes []state.MapKey
	for _, pl := range plan {
		key := state.MapKey{Source: pl.op.Source, Identity: pl.op.Identity}
		if pl.op.Action == target.ActionDelete {
			delete(upserts, key)
			deletes = append(deletes, key)
			continue
		}
		if _, ok := upserts[key]; !ok {
			order = append(order, key)
		}
		upserts[key] = state.MapEntry{MapKey: key, TargetID: targetIDs[pl.op.Identity], Hash: pl.hash, LastSeenRun: opt.runID}
	}
	c := state.Commit{Checkpoint: opt.checkpoint}
	for _, k := range order {
		if e, ok := upserts[k]; ok {
			c.Upserts = append(c.Upserts, e)
		}
	}
	// A delete followed by a re-insert in one batch keeps the entry.
	for _, k := range deletes {
		if _, ok := upserts[k]; !ok {
			c.Deletes = append(c.Deletes, k)
		}
	}
	return c
}

// hash is an HMAC over canonical JSON of the data, with secret values
// included so a changed key is detected; plain hashes of secrets are never
// stored (design-core §8.3).
func (p *Pipeline) hash(data map[string]any) (string, error) {
	b, err := json.Marshal(canonical(data))
	if err != nil {
		return "", target.Permanent("data cannot be fingerprinted: %v", err)
	}
	return p.Hasher.Sum(secrets.New(string(b))), nil
}

// canonical replaces secrets with tagged plain values so json.Marshal (which
// sorts map keys) yields a stable, secret-sensitive encoding. The result is
// only fed to the HMAC, never stored or logged.
func canonical(v any) any {
	switch x := v.(type) {
	case map[string]any:
		m := make(map[string]any, len(x))
		for k, vv := range x {
			m[k] = canonical(vv)
		}
		return m
	case []any:
		s := make([]any, len(x))
		for i, vv := range x {
			s[i] = canonical(vv)
		}
		return s
	case secrets.Value:
		return map[string]string{"$secret": x.Reveal()}
	}
	return v
}

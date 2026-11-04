package pg

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"

	"github.com/hyperbolic-c/newapi2ACG/internal/model"
	"github.com/hyperbolic-c/newapi2ACG/internal/state"
)

type Store struct{ DB *sql.DB }

var _ state.Store = (*Store)(nil)
var _ state.Transaction = (*Tx)(nil)

func (s *Store) Begin(ctx context.Context, job, target string) (state.Transaction, error) {
	if s == nil || s.DB == nil || strings.TrimSpace(job) == "" || strings.TrimSpace(target) == "" {
		return nil, errors.New("state transaction requires database, job and target")
	}
	tx, err := s.DB.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelRepeatableRead})
	if err != nil {
		return nil, errors.New("begin state transaction failed")
	}
	return &Tx{ctx: ctx, sql: tx, job: job, target: target}, nil
}

type stateSQL interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
	QueryRowContext(context.Context, string, ...any) *sql.Row
	Commit() error
	Rollback() error
}

// Tx must not be used concurrently. Any method failure poisons Commit so a
// caller cannot accidentally commit the successful prefix of a failed batch.
type Tx struct {
	ctx         context.Context
	sql         stateSQL
	job, target string
	err         error
	closed      bool
}

func (t *Tx) fail(message string) error {
	if t.err == nil {
		t.err = errors.New(message)
	}
	return t.err
}
func (t *Tx) ready() error {
	if t.closed {
		return errors.New("state transaction is closed")
	}
	if t.err != nil {
		return t.err
	}
	if t.ctx.Err() != nil {
		return t.fail("state transaction canceled")
	}
	return nil
}
func (t *Tx) exec(q string, args ...any) (sql.Result, error) {
	if err := t.ready(); err != nil {
		return nil, err
	}
	r, err := t.sql.ExecContext(t.ctx, q, args...)
	if err != nil {
		return nil, t.fail("state database write failed")
	}
	return r, nil
}
func (t *Tx) Commit() error {
	if err := t.ready(); err != nil {
		if !t.closed {
			_ = t.sql.Rollback() // already failing; the original error wins
			t.closed = true
		}
		return err
	}
	t.closed = true
	if err := t.sql.Commit(); err != nil {
		return errors.New("state commit failed; outcome unknown")
	}
	return nil
}
func (t *Tx) Rollback() error {
	if t.closed {
		return nil
	}
	t.closed = true
	if err := t.sql.Rollback(); err != nil && !errors.Is(err, sql.ErrTxDone) {
		return errors.New("state rollback failed")
	}
	return nil
}

// hexDigestRE matches a full SHA-256 / HMAC-SHA256 hex digest.
var hexDigestRE = regexp.MustCompile(`^[0-9a-f]{64}$`)

func (t *Tx) SetFullSyncStart(g0 string) error {
	_, err := t.exec(`INSERT INTO migrator.checkpoints(job_id,g0,gtid_set) VALUES($1,$2,$2) ON CONFLICT(job_id) DO UPDATE SET g0=EXCLUDED.g0,gtid_set=EXCLUDED.gtid_set,updated_at=now()`, t.job, g0)
	return err
}
func (t *Tx) Advance(pos string) error {
	r, err := t.exec(`UPDATE migrator.checkpoints SET gtid_set=$2,updated_at=now() WHERE job_id=$1`, t.job, pos)
	if err != nil {
		return err
	}
	if n, err := r.RowsAffected(); err != nil || n != 1 {
		// Incremental sync must not start without a recorded full-sync start.
		return t.fail("binlog position advanced before full sync recorded its start")
	}
	return nil
}
func (t *Tx) Position() (string, error) {
	if err := t.ready(); err != nil {
		return "", err
	}
	var pos string
	err := t.sql.QueryRowContext(t.ctx, "SELECT gtid_set FROM migrator.checkpoints WHERE job_id=$1", t.job).Scan(&pos)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", t.fail("read checkpoint failed")
	}
	return pos, nil
}
func (t *Tx) Enqueue(ids []model.Identity, reason string) error {
	if reason == "" {
		return t.fail("queue reason is required")
	}
	for _, id := range ids {
		if id.Validate() != nil {
			return t.fail("invalid pending identity")
		}
		if _, err := t.exec(`INSERT INTO migrator.pending(job_id,target_id,source_identity,reason) VALUES($1,$2,$3,$4) ON CONFLICT(job_id,target_id,source_identity) DO UPDATE SET version=nextval('migrator.pending_version_seq'),reason=EXCLUDED.reason,enqueued_at=now()`, t.job, t.target, id.String(), reason); err != nil {
			return err
		}
	}
	return t.ready()
}
func (t *Tx) TakePending(limit int) ([]state.PendingItem, error) {
	if err := t.ready(); err != nil {
		return nil, err
	}
	if limit <= 0 {
		return nil, t.fail("pending limit must be positive")
	}
	rows, err := t.sql.QueryContext(t.ctx, "SELECT source_identity,version,reason FROM migrator.pending WHERE job_id=$1 AND target_id=$2 ORDER BY version,source_identity LIMIT $3", t.job, t.target, limit)
	if err != nil {
		return nil, t.fail("read pending failed")
	}
	defer func() { _ = rows.Close() }() // rows.Err is checked below
	var items []state.PendingItem
	for rows.Next() {
		var item state.PendingItem
		var id string
		if rows.Scan(&id, &item.Version, &item.Reason) != nil {
			return nil, t.fail("decode pending failed")
		}
		if item.Identity.UnmarshalText([]byte(id)) != nil {
			return nil, t.fail("invalid persisted identity")
		}
		items = append(items, item)
	}
	if rows.Err() != nil {
		return nil, t.fail("read pending failed")
	}
	return items, nil
}
func (t *Tx) Dequeue(items []state.PendingItem) error {
	for _, item := range items {
		if item.Version <= 0 || item.Identity.Validate() != nil {
			return t.fail("invalid pending receipt")
		}
		if _, err := t.exec("DELETE FROM migrator.pending WHERE job_id=$1 AND target_id=$2 AND source_identity=$3 AND version=$4", t.job, t.target, item.Identity.String(), item.Version); err != nil {
			return err
		}
	}
	return t.ready()
}

func prepareChange(c model.Change) (model.Resource, string, error) {
	if c.Resource.User != nil && c.Resource.User.Initialization != nil {
		return model.Resource{}, "", errors.New("password initialization receipt is not implemented")
	}
	if c.OpID == "" || c.IdemKey == "" {
		return model.Resource{}, "", errors.New("operation identifiers required")
	}
	switch c.Op {
	case model.Create, model.Update, model.Disable, model.Delete, model.Grant, model.Revoke:
	default:
		return model.Resource{}, "", errors.New("invalid operation")
	}
	// Roundtrip makes a private copy, excludes initialization-only credentials.
	data, err := json.Marshal(c.Resource)
	if err != nil {
		return model.Resource{}, "", err
	}
	r, err := model.Decode(data)
	if err != nil {
		return model.Resource{}, "", err
	}
	if c.Op == model.Disable {
		switch r.Kind {
		case model.ProviderKind:
			r.Provider.Enabled = false
		case model.UserKind:
			r.User.Enabled = false
		case model.ApiKeyKind:
			r.ApiKey.Enabled = false
		default:
			return model.Resource{}, "", errors.New("resource cannot be disabled")
		}
	}
	hash, err := model.Hash(r)
	return r, hash, err
}
func (t *Tx) RecordIntent(changes []model.Change) error {
	seen := map[string]bool{}
	for _, c := range changes {
		if seen[c.OpID] {
			return t.fail("duplicate operation ID")
		}
		seen[c.OpID] = true
		r, hash, err := prepareChange(c)
		if err != nil {
			return t.fail("invalid operation intent")
		}
		result, err := t.exec(`INSERT INTO migrator.operations(job_id,target_id,op_id,source_identity,op,idem_key,desired_hash) VALUES($1,$2,$3,$4,$5,$6,$7) ON CONFLICT(job_id,op_id) DO NOTHING`, t.job, t.target, c.OpID, r.Metadata.SourceIdentity.String(), string(c.Op), c.IdemKey, hash)
		if err != nil {
			return err
		}
		n, err := result.RowsAffected()
		if err != nil {
			return t.fail("read intent registration result failed")
		}
		if n == 0 {
			verified, err := t.exec(`UPDATE migrator.operations SET op_id=op_id WHERE job_id=$1 AND target_id=$2 AND op_id=$3 AND source_identity=$4 AND op=$5 AND idem_key=$6 AND desired_hash=$7`, t.job, t.target, c.OpID, r.Metadata.SourceIdentity.String(), string(c.Op), c.IdemKey, hash)
			if err != nil {
				return err
			}
			matched, err := verified.RowsAffected()
			if err != nil || matched != 1 {
				return t.fail("operation ID conflicts with registered intent")
			}
		} else {
			if _, err := t.exec(`INSERT INTO migrator.resource_intents(job_id,target_id,source_identity,op_id) VALUES($1,$2,$3,$4) ON CONFLICT(job_id,target_id,source_identity) DO UPDATE SET op_id=EXCLUDED.op_id`, t.job, t.target, r.Metadata.SourceIdentity.String(), c.OpID); err != nil {
				return err
			}
		}
		if n != 0 {
			superseded, err := t.exec(`UPDATE migrator.operations SET status='failed',error_code='superseded',updated_at=now() WHERE job_id=$1 AND target_id=$2 AND source_identity=$3 AND op_id<>$4 AND status IN ('pending','unknown')`, t.job, t.target, r.Metadata.SourceIdentity.String(), c.OpID)
			if err != nil {
				return err
			}
			count, err := superseded.RowsAffected()
			if err != nil {
				return t.fail("read superseded intent count failed")
			}
			if count > 0 {
				if err := t.Enqueue([]model.Identity{r.Metadata.SourceIdentity}, "superseded_intent"); err != nil {
					return err
				}
			}
		}
		if c.Prev != nil {
			if c.Prev.Metadata.SourceIdentity != r.Metadata.SourceIdentity || c.Prev.Validate() != nil {
				return t.fail("invalid previous resource")
			}
			data, err := json.Marshal(c.Prev)
			if err != nil {
				return t.fail("invalid previous resource")
			}
			if _, err = t.exec(`INSERT INTO migrator.target_snapshots(job_id,target_id,op_id,source_identity,resource) SELECT $1,$2,$3,$4,$5::jsonb WHERE NOT EXISTS(SELECT 1 FROM migrator.target_snapshots WHERE job_id=$1 AND op_id=$3)`, t.job, t.target, c.OpID, r.Metadata.SourceIdentity.String(), string(data)); err != nil {
				return err
			}
		}
	}
	return t.ready()
}
func (t *Tx) LastApplied(ids []model.Identity) (map[model.Identity]model.Resource, error) {
	if err := t.ready(); err != nil {
		return nil, err
	}
	out := map[model.Identity]model.Resource{}
	for _, id := range ids {
		if id.Validate() != nil {
			return nil, t.fail("invalid resource identity")
		}
		var data []byte
		err := t.sql.QueryRowContext(t.ctx, "SELECT resource FROM migrator.last_applied WHERE job_id=$1 AND target_id=$2 AND source_identity=$3", t.job, t.target, id.String()).Scan(&data)
		if errors.Is(err, sql.ErrNoRows) {
			continue
		}
		if err != nil {
			return nil, t.fail("read last applied failed")
		}
		r, err := model.Decode(data)
		if err != nil {
			return nil, t.fail("invalid persisted resource")
		}
		out[id] = r
	}
	return out, nil
}
func resourceName(r model.Resource) string {
	switch r.Kind {
	case model.GroupKind:
		return r.Group.Name
	case model.ProviderKind:
		return r.Provider.Name
	case model.AggregateRouteKind:
		return r.AggregateRoute.Name
	case model.UserKind:
		return r.User.Username
	case model.ApiKeyKind:
		return r.ApiKey.Name
	}
	return ""
}
func (t *Tx) RecordResults(items []state.AppliedResult) error {
	if err := t.ready(); err != nil {
		return err
	}
	seen := map[string]bool{}
	for _, item := range items {
		c, res := item.Change, item.Result
		r, hash, err := prepareChange(c)
		if err != nil || res.OpID != c.OpID || seen[res.OpID] {
			return t.fail("invalid operation result")
		}
		seen[res.OpID] = true
		var status, storedTarget string
		err = t.sql.QueryRowContext(t.ctx, `SELECT status,COALESCE(target_resource_id,'') FROM migrator.operations WHERE job_id=$1 AND target_id=$2 AND op_id=$3 AND source_identity=$4 AND op=$5 AND idem_key=$6 AND desired_hash=$7 FOR UPDATE`, t.job, t.target, c.OpID, r.Metadata.SourceIdentity.String(), string(c.Op), c.IdemKey, hash).Scan(&status, &storedTarget)
		if err != nil {
			return t.fail("result does not match registered intent")
		}
		next := ""
		switch res.Outcome {
		case model.Succeeded:
			next = "succeeded"
		case model.Failed:
			next = "failed"
		case model.Unknown, model.Retryable:
			next = "unknown"
		default:
			return t.fail("invalid result outcome")
		}
		if status == "succeeded" {
			if next != "succeeded" || storedTarget != res.TargetID {
				return t.fail("successful receipt is immutable")
			}
			continue
		}
		var currentOp string
		err = t.sql.QueryRowContext(t.ctx, `SELECT op_id FROM migrator.resource_intents WHERE job_id=$1 AND target_id=$2 AND source_identity=$3 FOR UPDATE`, t.job, t.target, r.Metadata.SourceIdentity.String()).Scan(&currentOp)
		if err != nil || currentOp != c.OpID {
			return t.fail("result belongs to a superseded intent; observe and recompute current state")
		}
		if next == "succeeded" {
			if !hexDigestRE.MatchString(item.ConfigHash) || (res.TargetID == "" && c.Op != model.Delete) {
				return t.fail("successful result requires config hash and target ID")
			}
			id := r.Metadata.SourceIdentity.String()
			if c.Op == model.Delete {
				for _, table := range []string{"id_map", "last_applied", "key_access"} {
					if _, err = t.exec(fmt.Sprintf("DELETE FROM migrator.%s WHERE job_id=$1 AND target_id=$2 AND source_identity=$3", table), t.job, t.target, id); err != nil {
						return err
					}
				}
			} else {
				data, err := json.Marshal(r)
				if err != nil {
					return t.fail("invalid applied resource")
				}
				if _, err = t.exec(`INSERT INTO migrator.id_map(job_id,target_id,source_identity,target_resource_id,target_name) VALUES($1,$2,$3,$4,$5) ON CONFLICT(job_id,target_id,source_identity) DO UPDATE SET target_resource_id=EXCLUDED.target_resource_id,target_name=EXCLUDED.target_name`, t.job, t.target, id, res.TargetID, resourceName(r)); err != nil {
					return err
				}
				if _, err = t.exec(`INSERT INTO migrator.last_applied(job_id,target_id,source_identity,resource,content_hash,config_hash) VALUES($1,$2,$3,$4::jsonb,$5,$6) ON CONFLICT(job_id,target_id,source_identity) DO UPDATE SET resource=EXCLUDED.resource,content_hash=EXCLUDED.content_hash,config_hash=EXCLUDED.config_hash,updated_at=now()`, t.job, t.target, id, string(data), hash, item.ConfigHash); err != nil {
					return err
				}
				if r.ApiKey != nil {
					routes, _ := json.Marshal(r.ApiKey.RouteRefs)
					providers, _ := json.Marshal(r.ApiKey.ProviderRefs)
					if _, err = t.exec(`INSERT INTO migrator.key_access(job_id,target_id,source_identity,route_refs,provider_refs) VALUES($1,$2,$3,$4::jsonb,$5::jsonb) ON CONFLICT(job_id,target_id,source_identity) DO UPDATE SET route_refs=EXCLUDED.route_refs,provider_refs=EXCLUDED.provider_refs`, t.job, t.target, id, string(routes), string(providers)); err != nil {
						return err
					}
				}
			}
		}
		if _, err = t.exec("UPDATE migrator.operations SET status=$4,target_resource_id=$5,error_code=$6,updated_at=now() WHERE job_id=$1 AND target_id=$2 AND op_id=$3", t.job, t.target, c.OpID, next, res.TargetID, string(res.Outcome)); err != nil {
			return err
		}
	}
	return t.ready()
}

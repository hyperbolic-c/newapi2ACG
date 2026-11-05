package postgres

import (
	"context"
	"database/sql"
	"errors"

	"github.com/lib/pq"

	"github.com/hyperbolic-c/newapi2ACG/internal/state"
	"github.com/hyperbolic-c/newapi2ACG/internal/target"
)

func (s *Store) writeEntries(ctx context.Context, tx *sql.Tx, task string, c state.Commit) error {
	for _, e := range c.Upserts {
		_, err := tx.ExecContext(ctx, `INSERT INTO `+s.table("migration_resource_map")+`
(task_id,source_dataset,source_key,target_kind,target_key,target_id,last_applied_hash,last_seen_run,updated_at)
VALUES($1,$2,$3,$4,$5,$6,$7,$8,now())
ON CONFLICT (task_id,target_kind,target_key,source_dataset,source_key) DO UPDATE SET
 target_id = CASE WHEN EXCLUDED.target_id = '' THEN migration_resource_map.target_id ELSE EXCLUDED.target_id END,
 last_seen_run = CASE WHEN EXCLUDED.last_seen_run = '' THEN migration_resource_map.last_seen_run ELSE EXCLUDED.last_seen_run END,
 last_applied_hash = EXCLUDED.last_applied_hash, updated_at = now()`,
			task, e.Source.Dataset, e.Source.Key, e.Identity.Kind, e.Identity.ID, e.TargetID, e.Hash, e.LastSeenRun)
		if err != nil {
			return errors.New("write resource map failed")
		}
	}
	for _, k := range c.Deletes {
		_, err := tx.ExecContext(ctx, `DELETE FROM `+s.table("migration_resource_map")+`
WHERE task_id=$1 AND target_kind=$2 AND target_key=$3 AND source_dataset=$4 AND source_key=$5`,
			task, k.Identity.Kind, k.Identity.ID, k.Source.Dataset, k.Source.Key)
		if err != nil {
			return errors.New("delete resource map entry failed")
		}
	}
	return nil
}

func (s *Store) Hashes(ctx context.Context, task string, ids []target.TargetIdentity) (map[target.TargetIdentity]string, error) {
	out := map[target.TargetIdentity]string{}
	if len(ids) == 0 {
		return out, nil
	}
	kinds := make([]string, len(ids))
	keys := make([]string, len(ids))
	for i, id := range ids {
		kinds[i], keys[i] = id.Kind, id.ID
	}
	rows, err := s.db.QueryContext(ctx, `SELECT m.target_kind, m.target_key, m.last_applied_hash
FROM `+s.table("migration_resource_map")+` m
JOIN unnest($2::text[], $3::text[]) AS w(kind, key) ON m.target_kind = w.kind AND m.target_key = w.key
WHERE m.task_id = $1`, task, pq.Array(kinds), pq.Array(keys))
	if err != nil {
		return nil, errors.New("read resource map failed")
	}
	defer func() { _ = rows.Close() }() // rows.Err is checked below
	for rows.Next() {
		var id target.TargetIdentity
		var hash string
		if err := rows.Scan(&id.Kind, &id.ID, &hash); err != nil {
			return nil, errors.New("read resource map failed")
		}
		out[id] = hash
	}
	if rows.Err() != nil {
		return nil, errors.New("read resource map failed")
	}
	return out, nil
}

func (s *Store) Unseen(ctx context.Context, task, run string, datasets []string) ([]state.MapEntry, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT source_dataset,source_key,target_kind,target_key,target_id,last_applied_hash,last_seen_run
FROM `+s.table("migration_resource_map")+`
WHERE task_id=$1 AND last_seen_run <> $2 AND source_dataset = ANY($3)
ORDER BY source_dataset, source_key`, task, run, pq.Array(datasets))
	if err != nil {
		return nil, errors.New("read resource map failed")
	}
	defer func() { _ = rows.Close() }() // rows.Err is checked below
	var out []state.MapEntry
	for rows.Next() {
		var e state.MapEntry
		if err := rows.Scan(&e.Source.Dataset, &e.Source.Key, &e.Identity.Kind, &e.Identity.ID, &e.TargetID, &e.Hash, &e.LastSeenRun); err != nil {
			return nil, errors.New("read resource map failed")
		}
		out = append(out, e)
	}
	if rows.Err() != nil {
		return nil, errors.New("read resource map failed")
	}
	return out, nil
}

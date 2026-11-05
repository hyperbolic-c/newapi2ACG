-- Sync state only (design-core §8). No copy of source data is stored.
CREATE TABLE {{schema}}.migration_tasks (
  task_id     text PRIMARY KEY,
  source_id   text NOT NULL,
  phase       text NOT NULL CHECK (phase IN ('initializing','snapshotting','catching_up','streaming','reconciling','paused','failed')),
  config_hash text NOT NULL DEFAULT '',
  last_error  text NOT NULL DEFAULT '',
  lease_owner text,
  lease_until timestamptz,
  updated_at  timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE {{schema}}.migration_checkpoints (
  task_id           text PRIMARY KEY REFERENCES {{schema}}.migration_tasks(task_id),
  snapshot_position jsonb NOT NULL DEFAULT '{}',
  applied_position  jsonb NOT NULL DEFAULT '{}',
  snapshot_done     boolean NOT NULL DEFAULT false,
  reconcile_run_id  text NOT NULL DEFAULT '',
  updated_at        timestamptz NOT NULL DEFAULT now()
);

-- target_key is the mapper's logical identity; target_id is the ACG ID.
-- One source row may produce several target resources.
CREATE TABLE {{schema}}.migration_resource_map (
  task_id           text NOT NULL REFERENCES {{schema}}.migration_tasks(task_id),
  source_dataset    text NOT NULL,
  source_key        text NOT NULL,
  target_kind       text NOT NULL,
  target_key        text NOT NULL,
  target_id         text NOT NULL DEFAULT '',
  last_applied_hash text NOT NULL,
  last_seen_run     text NOT NULL DEFAULT '',
  updated_at        timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (task_id, target_kind, target_key, source_dataset, source_key)
);
CREATE INDEX migration_resource_map_source ON {{schema}}.migration_resource_map (task_id, source_dataset, source_key);

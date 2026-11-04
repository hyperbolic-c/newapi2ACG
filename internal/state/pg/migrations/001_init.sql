-- The service keeps no copy of New API data (design v3.3, 8.7). Credentials
-- are never stored; JSONB holds redacted resource content and salted hashes.
CREATE TABLE migrator.jobs (
 job_id text PRIMARY KEY,
 definition jsonb NOT NULL DEFAULT '{}',
 state text NOT NULL DEFAULT 'PRECHECK' CHECK (state IN ('PRECHECK','FULL_SYNC','STREAMING','POLLING','PAUSED','STOPPED')),
 resume_from text, reason text,
 config_hash text,
 initial_sync_completed_at timestamptz,
 last_reconciliation jsonb,
 updated_at timestamptz NOT NULL DEFAULT now()
);
-- One row per job: g0 is the binlog position recorded before the full read;
-- gtid_set is the position incremental sync has processed up to.
CREATE TABLE migrator.checkpoints (
 job_id text PRIMARY KEY REFERENCES migrator.jobs(job_id),
 g0 text NOT NULL DEFAULT '', gtid_set text NOT NULL DEFAULT '',
 updated_at timestamptz NOT NULL DEFAULT now()
);
-- Global sequence prevents version reuse after dequeue/re-enqueue (ABA).
CREATE SEQUENCE migrator.pending_version_seq AS bigint;
CREATE TABLE migrator.pending (
 job_id text NOT NULL REFERENCES migrator.jobs(job_id), target_id text NOT NULL, source_identity text NOT NULL,
 version bigint NOT NULL DEFAULT nextval('migrator.pending_version_seq') CHECK(version > 0),
 reason text NOT NULL, enqueued_at timestamptz NOT NULL DEFAULT now(),
 PRIMARY KEY(job_id,target_id,source_identity)
);
CREATE TABLE migrator.naming_registry (
 job_id text NOT NULL REFERENCES migrator.jobs(job_id), target_id text NOT NULL, kind text NOT NULL,
 name text NOT NULL, source_identity text NOT NULL, frozen boolean NOT NULL DEFAULT false,
 PRIMARY KEY(target_id,kind,name), UNIQUE(target_id,source_identity)
);
CREATE TABLE migrator.id_map (
 job_id text NOT NULL REFERENCES migrator.jobs(job_id), target_id text NOT NULL, source_identity text NOT NULL,
 target_resource_id text NOT NULL, target_name text NOT NULL, password_initialized_at timestamptz,
 PRIMARY KEY(job_id,target_id,source_identity), UNIQUE(target_id,source_identity)
);
CREATE TABLE migrator.last_applied (
 job_id text NOT NULL REFERENCES migrator.jobs(job_id), target_id text NOT NULL, source_identity text NOT NULL,
 resource jsonb NOT NULL, content_hash text NOT NULL, config_hash text NOT NULL,
 updated_at timestamptz NOT NULL DEFAULT now(), PRIMARY KEY(job_id,target_id,source_identity)
);
CREATE TABLE migrator.key_access (
 job_id text NOT NULL REFERENCES migrator.jobs(job_id), target_id text NOT NULL, source_identity text NOT NULL,
 route_refs jsonb NOT NULL DEFAULT '[]', provider_refs jsonb NOT NULL DEFAULT '[]',
 PRIMARY KEY(job_id,target_id,source_identity)
);
CREATE TABLE migrator.operations (
 job_id text NOT NULL REFERENCES migrator.jobs(job_id), target_id text NOT NULL, op_id text NOT NULL,
 source_identity text NOT NULL, op text NOT NULL CHECK(op IN ('create','update','disable','delete','grant','revoke')),
 idem_key text NOT NULL, desired_hash text NOT NULL,
 status text NOT NULL DEFAULT 'pending' CHECK(status IN ('pending','succeeded','failed','unknown')),
 target_resource_id text, error_code text,
 created_at timestamptz NOT NULL DEFAULT now(), updated_at timestamptz NOT NULL DEFAULT now(),
 PRIMARY KEY(job_id,op_id)
);
CREATE INDEX operations_unresolved ON migrator.operations(job_id,target_id,status) WHERE status IN ('pending','unknown');
CREATE TABLE migrator.failed_items (
 job_id text NOT NULL REFERENCES migrator.jobs(job_id), target_id text NOT NULL, source_identity text NOT NULL,
 config_hash text NOT NULL, desired_hash text NOT NULL, error_type text NOT NULL, error_message text NOT NULL,
 blocked_by jsonb NOT NULL DEFAULT '[]', retry_count integer NOT NULL DEFAULT 0 CHECK(retry_count >= 0), next_retry_at timestamptz,
 PRIMARY KEY(job_id,target_id,source_identity)
);
CREATE TABLE migrator.target_snapshots (
 snapshot_id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
 job_id text NOT NULL, target_id text NOT NULL, op_id text NOT NULL, source_identity text NOT NULL,
 resource jsonb NOT NULL, created_at timestamptz NOT NULL DEFAULT now(),
 FOREIGN KEY(job_id,op_id) REFERENCES migrator.operations(job_id,op_id)
);
CREATE TABLE migrator.config_versions (
 job_id text NOT NULL REFERENCES migrator.jobs(job_id), config_hash text NOT NULL,
 config jsonb NOT NULL, activated_at timestamptz NOT NULL DEFAULT now(), PRIMARY KEY(job_id,config_hash)
);
CREATE TABLE migrator.audit_log (
 audit_id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
 job_id text NOT NULL REFERENCES migrator.jobs(job_id), actor text NOT NULL,
 action text NOT NULL, details jsonb NOT NULL DEFAULT '{}', created_at timestamptz NOT NULL DEFAULT now()
);

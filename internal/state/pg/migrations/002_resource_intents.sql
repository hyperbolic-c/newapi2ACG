-- Tracks the newest registered intent independently of content hashes. An old
-- result must not overwrite a later intent, including delete/create cycles.
CREATE TABLE migrator.resource_intents (
 job_id text NOT NULL,
 target_id text NOT NULL,
 source_identity text NOT NULL,
 op_id text NOT NULL,
 PRIMARY KEY(job_id,target_id,source_identity),
 FOREIGN KEY(job_id,op_id) REFERENCES migrator.operations(job_id,op_id)
);

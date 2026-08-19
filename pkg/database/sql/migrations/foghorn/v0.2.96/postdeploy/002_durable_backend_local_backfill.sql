-- Backfill billing attribution for rows already synced before durable_backend_local existed. Only rows whose
-- bytes are UNAMBIGUOUSLY local are marked: BOTH storage_cluster_id AND origin_cluster_id must be NULL. A
-- NULL storage_cluster_id alone is NOT sufficient — the schema defines it as "same as origin_cluster_id", and
-- a playback-federation adopted-remote row can carry a REMOTE origin_cluster_id while leaving
-- storage_cluster_id NULL; marking those would bill this provider for another cluster's bytes. Rows with any
-- cluster attribution are left false; if they are in fact local they re-attribute on their next local write,
-- and an identity-aware service reconciliation (aware of THIS cell/backend) can claim the ambiguous remainder.
-- Runs in POSTDEPLOY (a bulk UPDATE is not expand-phase safe); idempotent, so re-running is a no-op.
UPDATE foghorn.artifacts
   SET durable_backend_local = true
 WHERE sync_status = 'synced'
   AND storage_cluster_id IS NULL
   AND origin_cluster_id IS NULL
   AND durable_backend_local = false;

-- Incremental .dtsh sync attempt identity. A .dtsh sync runs on an ALREADY-SYNCED artifact (the main
-- upload finished; the Mist .dtsh index arrived after), so it cannot reuse the main-upload attempt on
-- sync_status/sync_request_id. This is its OWN attempt: set to 'in_progress' with the request/node
-- when TriggerDtshSync dispatches, and a DtshSync completion (success OR failure) is applied ONLY when
-- it matches this exact attempt — so a stale/duplicate/wrong-node dtsh completion can neither flip
-- dtsh_synced nor advance chapter reclaim. Cleared on any terminal transition; a 'failed' attempt is
-- retryable and dtsh_failure_count drives backoff.
--
-- Schema source of truth: pkg/database/sql/schema/foghorn.sql — same columns in the baseline so a
-- fresh init and an upgrade converge.
ALTER TABLE foghorn.artifacts ADD COLUMN IF NOT EXISTS dtsh_status VARCHAR(20);
ALTER TABLE foghorn.artifacts ADD COLUMN IF NOT EXISTS dtsh_sync_request_id VARCHAR(200);
ALTER TABLE foghorn.artifacts ADD COLUMN IF NOT EXISTS dtsh_sync_node_id VARCHAR(100);
ALTER TABLE foghorn.artifacts ADD COLUMN IF NOT EXISTS dtsh_last_attempt TIMESTAMP;
ALTER TABLE foghorn.artifacts ADD COLUMN IF NOT EXISTS dtsh_failure_count INT NOT NULL DEFAULT 0;

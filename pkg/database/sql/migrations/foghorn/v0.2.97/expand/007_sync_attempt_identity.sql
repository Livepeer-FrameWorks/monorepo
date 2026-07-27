-- Sync-attempt identity. When an artifact transitions to sync_status='in_progress' (freeze
-- permission granted / reconciler dispatch) it records the outstanding attempt's request id and the
-- node it was dispatched to. A SyncComplete is applied ONLY when it matches this
-- attempt AND the row is still in_progress — so a duplicate completion, a completion from a
-- superseded attempt, or a completion for the wrong node is a guarded no-op instead of re-applying
-- state (double node-copy, resurrected lifecycle history, wrong-node attribution). Both columns are
-- cleared on every terminal transition (synced / failed / lost_local) and by stale recovery.
--
-- Schema source of truth: pkg/database/sql/schema/foghorn.sql — same columns in the baseline so a
-- fresh init and an upgrade converge.
ALTER TABLE foghorn.artifacts ADD COLUMN IF NOT EXISTS sync_request_id VARCHAR(200);
ALTER TABLE foghorn.artifacts ADD COLUMN IF NOT EXISTS sync_node_id VARCHAR(100);

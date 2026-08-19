-- Retention-GC scan index for foghorn.artifact_creation_commands: consumed terminal
-- ('committed'/'rejected') rows oldest-first by consumed_at. Partial on the exact GC
-- predicate (terminal AND consumed_at IS NOT NULL) so the bounded delete pass — which
-- orders and range-filters on consumed_at, the retention anchor — never scans the
-- unconsumed rows it must retain, nor degrades with total historical volume. Anchoring on
-- consumed_at (not updated_at) means a row terminalized long ago but only just consumed is
-- retained a full horizon past its ack, not immediately GC-eligible. Without it the GC's
-- only usable index was the partial WHERE status='accepted' one, which does not cover the
-- terminal-row delete.
--
-- Schema source of truth: pkg/database/sql/schema/foghorn.sql — same index in the baseline
-- so a fresh init and an upgrade converge.
CREATE INDEX IF NOT EXISTS idx_foghorn_creation_commands_terminal_gc
    ON foghorn.artifact_creation_commands(consumed_at)
    WHERE status IN ('committed', 'rejected') AND consumed_at IS NOT NULL;

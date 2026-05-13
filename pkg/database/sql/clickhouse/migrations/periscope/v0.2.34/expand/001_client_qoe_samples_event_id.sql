-- Adds a per-sample event_id to client_qoe_samples so replay-dedup audits can
-- distinguish replayed rows from genuine duplicates. Nullable with DEFAULT NULL
-- on purpose: a server-side generateUUIDv4() default would mint a fresh UUID
-- per replayed row and make `count() - uniqExact(event_id)` always look clean.
-- Helmsman generates the UUID per sample before Foghorn batches it; older rows
-- written before this column existed stay NULL and are filtered out of audits
-- via `event_id IS NOT NULL`.
ALTER TABLE client_qoe_samples
    ADD COLUMN IF NOT EXISTS event_id Nullable(UUID) DEFAULT NULL AFTER timestamp;

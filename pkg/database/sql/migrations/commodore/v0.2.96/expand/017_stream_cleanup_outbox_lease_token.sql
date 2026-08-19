-- Token-fence the stream-cleanup outbox: the claim stamps a fresh lease_token, and every settlement CAS-checks it,
-- so a stale worker (its schedule-based lease lapsed and a peer re-claimed the row with a new token) can no longer
-- settle a row it no longer owns. Additive; NULL keeps the existing schedule-only lease.
ALTER TABLE commodore.stream_cleanup_outbox
    ADD COLUMN IF NOT EXISTS lease_token TEXT;

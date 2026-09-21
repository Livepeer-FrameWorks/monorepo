-- Per-artifact revision stamped as the aggregate version of the artifact's
-- domain events. Every transition that publishes a clip, recording, or upload
-- event increments it in its own transaction, so versions of one artifact
-- follow commit order across Foghorn replicas. Existing rows start at 0; their
-- next transition carries 1.
ALTER TABLE foghorn.artifacts ADD COLUMN IF NOT EXISTS revision BIGINT NOT NULL DEFAULT 0;

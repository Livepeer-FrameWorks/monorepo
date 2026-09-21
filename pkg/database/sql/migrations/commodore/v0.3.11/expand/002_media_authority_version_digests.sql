-- content_digest identifies what an authority says independent of per-compile
-- entropy, so an unchanged recompile publishes nothing. dependents_digest covers
-- the tenant fields media objects are derived from and decides object fan-out.
ALTER TABLE commodore.media_authority_versions
    ADD COLUMN IF NOT EXISTS content_digest BYTEA;
ALTER TABLE commodore.media_authority_versions
    ADD COLUMN IF NOT EXISTS dependents_digest BYTEA;
-- A tombstone is terminal and never renewed, so renewal repair and the operator
-- health check must tell it apart without decoding the payload.
ALTER TABLE commodore.media_authority_versions
    ADD COLUMN IF NOT EXISTS tombstone BOOLEAN NOT NULL DEFAULT FALSE;

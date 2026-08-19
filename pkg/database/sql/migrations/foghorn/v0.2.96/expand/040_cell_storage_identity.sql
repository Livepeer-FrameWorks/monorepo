-- The cell's committed local S3 backend descriptor, enforced immutable at startup. Backend
-- REPOINTING (changing bucket/endpoint/region/prefix) is not a supported operation: historical
-- cleanup routes by the object's recorded backend, and this cell wires only its current store, so a silent repoint
-- would delete from — or fail closed on — the wrong backend. Foghorn records its descriptor here on first boot and
-- refuses to start if the descriptor later differs (credentials are NOT part of the identity, so key rotation is
-- fine). This makes unchanged-S3 support a code-enforced invariant rather than an operator promise. Single row.
CREATE TABLE IF NOT EXISTS foghorn.cell_storage_identity (
    id           BOOLEAN PRIMARY KEY DEFAULT true CHECK (id),  -- single-row guard
    backend_id   TEXT NOT NULL,                                -- BackendFingerprint(kind,bucket,endpoint,region,prefix)
    bucket       TEXT NOT NULL,
    endpoint     TEXT NOT NULL,
    region       TEXT NOT NULL,
    prefix       TEXT NOT NULL,
    committed_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

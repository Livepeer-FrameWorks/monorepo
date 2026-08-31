-- Persist a bounded Quartermaster-authenticated node identity in the media
-- cell. A stable fingerprint selects the binding, while proof-of-possession
-- of the enrolled node key authenticates each reconnect.
CREATE TABLE IF NOT EXISTS foghorn.node_admissions (
    canonical_node_id  VARCHAR(100) PRIMARY KEY,
    fingerprint_sha256 BYTEA NOT NULL UNIQUE CHECK (octet_length(fingerprint_sha256) = 32),
    public_key_ed25519  BYTEA NOT NULL,
    tenant_id          UUID NOT NULL,
    cluster_id         VARCHAR(255) NOT NULL CHECK (btrim(cluster_id) <> ''),
    validated_at       TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    valid_until        TIMESTAMPTZ NOT NULL,
    updated_at         TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CONSTRAINT ck_foghorn_node_admissions_public_key_present
        CHECK (public_key_ed25519 IS NOT NULL),
    CONSTRAINT ck_foghorn_node_admissions_public_key_shape
        CHECK (public_key_ed25519 IS NULL OR octet_length(public_key_ed25519) = 32),
    CONSTRAINT ck_foghorn_node_admissions_valid_until_present
        CHECK (valid_until IS NOT NULL)
);

ALTER TABLE foghorn.node_admissions
    ADD COLUMN IF NOT EXISTS public_key_ed25519 BYTEA,
    ADD COLUMN IF NOT EXISTS valid_until TIMESTAMPTZ;

DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM pg_constraint
        WHERE conrelid = 'foghorn.node_admissions'::regclass
          AND conname = 'ck_foghorn_node_admissions_public_key_present'
    ) THEN
        ALTER TABLE foghorn.node_admissions
            ADD CONSTRAINT ck_foghorn_node_admissions_public_key_present
            CHECK (public_key_ed25519 IS NOT NULL) NOT VALID;
    END IF;
    IF NOT EXISTS (
        SELECT 1 FROM pg_constraint
        WHERE conrelid = 'foghorn.node_admissions'::regclass
          AND conname = 'ck_foghorn_node_admissions_public_key_shape'
    ) THEN
        ALTER TABLE foghorn.node_admissions
            ADD CONSTRAINT ck_foghorn_node_admissions_public_key_shape
            CHECK (public_key_ed25519 IS NULL OR octet_length(public_key_ed25519) = 32) NOT VALID;
    END IF;
    IF NOT EXISTS (
        SELECT 1 FROM pg_constraint
        WHERE conrelid = 'foghorn.node_admissions'::regclass
          AND conname = 'ck_foghorn_node_admissions_valid_until_present'
    ) THEN
        ALTER TABLE foghorn.node_admissions
            ADD CONSTRAINT ck_foghorn_node_admissions_valid_until_present
            CHECK (valid_until IS NOT NULL) NOT VALID;
    END IF;
END
$$;

CREATE TABLE IF NOT EXISTS foghorn.node_admission_proof_nonces (
    public_key_sha256 BYTEA NOT NULL CHECK (octet_length(public_key_sha256) = 32),
    nonce             BYTEA NOT NULL CHECK (octet_length(nonce) = 32),
    proof_issued_at   TIMESTAMPTZ NOT NULL,
    expires_at        TIMESTAMPTZ NOT NULL,
    PRIMARY KEY (public_key_sha256, nonce)
);

CREATE INDEX IF NOT EXISTS idx_foghorn_node_admission_proof_nonces_expiry
    ON foghorn.node_admission_proof_nonces(expires_at);

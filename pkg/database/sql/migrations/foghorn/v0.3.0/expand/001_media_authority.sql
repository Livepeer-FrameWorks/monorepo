CREATE TABLE IF NOT EXISTS foghorn.media_authorities (
    authority_kind VARCHAR(32) NOT NULL,
    authority_id VARCHAR(255) NOT NULL,
    authority_version BIGINT NOT NULL CHECK (authority_version > 0),
    signer_key_id VARCHAR(255) NOT NULL,
    audience_cell_id VARCHAR(255) NOT NULL,
    issued_at TIMESTAMPTZ NOT NULL,
    refresh_after TIMESTAMPTZ NOT NULL,
    valid_until TIMESTAMPTZ NOT NULL,
    payload_sha256 BYTEA NOT NULL CHECK (octet_length(payload_sha256) = 32),
    signed_envelope BYTEA NOT NULL,
    payload BYTEA NOT NULL,
    source_revisions JSONB NOT NULL DEFAULT '[]'::jsonb,
    applied_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (authority_kind, authority_id),
    CONSTRAINT chk_foghorn_media_authority_kind
        CHECK (authority_kind IN ('tenant', 'media_object')),
    CONSTRAINT chk_foghorn_media_authority_times
        CHECK (issued_at <= refresh_after AND refresh_after < valid_until),
    CONSTRAINT chk_foghorn_media_authority_audience
        CHECK (btrim(audience_cell_id) <> ''),
    CONSTRAINT chk_foghorn_media_authority_source_revisions
        CHECK (jsonb_typeof(source_revisions) = 'array')
);

CREATE INDEX IF NOT EXISTS idx_foghorn_media_authorities_refresh
    ON foghorn.media_authorities(refresh_after);

CREATE INDEX IF NOT EXISTS idx_foghorn_media_authorities_expiry
    ON foghorn.media_authorities(valid_until);

CREATE TABLE IF NOT EXISTS foghorn.tenant_authority_projection (
    tenant_id UUID PRIMARY KEY,
    authority_version BIGINT NOT NULL CHECK (authority_version > 0),
    lifecycle VARCHAR(20) NOT NULL,
    billing_decision VARCHAR(32) NOT NULL,
    billing_model VARCHAR(20) NOT NULL,
    official_cluster_id VARCHAR(255),
    allow_platform_shared_playback BOOLEAN NOT NULL DEFAULT FALSE,
    max_streams INTEGER NOT NULL DEFAULT 0 CHECK (max_streams >= 0),
    max_viewers INTEGER NOT NULL DEFAULT 0 CHECK (max_viewers >= 0),
    allowances JSONB NOT NULL DEFAULT '[]'::jsonb,
    decision_reason TEXT NOT NULL DEFAULT '',
    local_read_ready BOOLEAN NOT NULL DEFAULT FALSE,
    local_ingest_ready BOOLEAN NOT NULL DEFAULT FALSE,
    local_source_ready BOOLEAN NOT NULL DEFAULT FALSE,
    valid_until TIMESTAMPTZ NOT NULL,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CONSTRAINT chk_tenant_authority_projection_lifecycle
        CHECK (lifecycle IN ('active', 'inactive', 'tombstone')),
    CONSTRAINT chk_tenant_authority_projection_billing
        CHECK (billing_decision IN ('allow', 'payment_required', 'suspended', 'inactive')),
    CONSTRAINT chk_tenant_authority_projection_model
        CHECK (billing_model IN ('unspecified', 'postpaid', 'prepaid')),
    CONSTRAINT chk_tenant_authority_allowances
        CHECK (jsonb_typeof(allowances) = 'array')
);

CREATE TABLE IF NOT EXISTS foghorn.tenant_authority_grants (
    tenant_id UUID NOT NULL REFERENCES foghorn.tenant_authority_projection(tenant_id) ON DELETE CASCADE,
    cluster_id VARCHAR(255) NOT NULL,
    authority_version BIGINT NOT NULL CHECK (authority_version > 0),
    access_source VARCHAR(50) NOT NULL,
    access_level VARCHAR(50) NOT NULL DEFAULT '',
    subscription_status VARCHAR(50) NOT NULL,
    cluster_class VARCHAR(50) NOT NULL DEFAULT '',
    cluster_type VARCHAR(50) NOT NULL DEFAULT '',
    deployment_model VARCHAR(50) NOT NULL DEFAULT '',
    owner_tenant_id UUID,
    expires_at TIMESTAMPTZ,
    PRIMARY KEY (tenant_id, cluster_id),
    CONSTRAINT chk_tenant_authority_grant_source
        CHECK (access_source IN ('platform_tier', 'owner', 'private_invite', 'marketplace_subscription', 'operator_override')),
    CONSTRAINT chk_tenant_authority_grant_status
        CHECK (subscription_status = 'active')
);

CREATE INDEX IF NOT EXISTS idx_tenant_authority_grants_cluster
    ON foghorn.tenant_authority_grants(cluster_id, tenant_id);

CREATE TABLE IF NOT EXISTS foghorn.media_object_authority_projection (
    authority_id VARCHAR(255) PRIMARY KEY,
    authority_version BIGINT NOT NULL CHECK (authority_version > 0),
    object_kind VARCHAR(20) NOT NULL,
    tenant_id UUID NOT NULL,
    user_id UUID,
    internal_name VARCHAR(255) NOT NULL,
    playback_id VARCHAR(255) NOT NULL,
    lifecycle VARCHAR(20) NOT NULL,
    origin_cluster_id VARCHAR(255),
    playback_policy_kind VARCHAR(20) NOT NULL,
    playback_policy BYTEA NOT NULL,
    stream_id UUID,
    ingest_mode VARCHAR(32),
    artifact_id VARCHAR(255),
    artifact_hash VARCHAR(255),
    artifact_kind VARCHAR(20),
    local_read_ready BOOLEAN NOT NULL DEFAULT FALSE,
    local_ingest_ready BOOLEAN NOT NULL DEFAULT FALSE,
    local_source_ready BOOLEAN NOT NULL DEFAULT FALSE,
    publishing_credential_sha256 BYTEA,
    valid_until TIMESTAMPTZ NOT NULL,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CONSTRAINT chk_media_object_authority_kind
        CHECK (object_kind IN ('live_stream', 'artifact')),
    CONSTRAINT chk_media_object_authority_lifecycle
        CHECK (lifecycle IN ('active', 'inactive', 'tombstone')),
    CONSTRAINT chk_media_object_authority_policy
        CHECK (playback_policy_kind IN ('public', 'jwt', 'webhook', 'deny')),
    CONSTRAINT chk_media_object_authority_publishing_credential
        CHECK (publishing_credential_sha256 IS NULL OR octet_length(publishing_credential_sha256) = 32),
    CONSTRAINT chk_media_object_authority_shape
        CHECK (
            (object_kind = 'live_stream' AND stream_id IS NOT NULL AND ingest_mode IS NOT NULL AND artifact_id IS NULL AND artifact_hash IS NULL AND artifact_kind IS NULL)
            OR
            (object_kind = 'artifact' AND stream_id IS NULL AND ingest_mode IS NULL AND artifact_id IS NOT NULL AND artifact_hash IS NOT NULL AND artifact_kind IN ('vod', 'dvr', 'clip', 'chapter'))
        )
);

CREATE UNIQUE INDEX IF NOT EXISTS uq_media_object_authority_active_internal_name
    ON foghorn.media_object_authority_projection(internal_name)
    WHERE lifecycle = 'active';

CREATE UNIQUE INDEX IF NOT EXISTS uq_media_object_authority_active_playback_id
    ON foghorn.media_object_authority_projection((lower(playback_id)))
    WHERE lifecycle = 'active';

-- Resolution must inspect tombstones as well as active rows, so the partial
-- uniqueness indexes above cannot serve the playback/source hot paths.
CREATE INDEX IF NOT EXISTS idx_media_object_authority_internal_name
    ON foghorn.media_object_authority_projection(internal_name);

CREATE INDEX IF NOT EXISTS idx_media_object_authority_playback_id
    ON foghorn.media_object_authority_projection((lower(playback_id)));

CREATE INDEX IF NOT EXISTS idx_media_object_authority_tenant
    ON foghorn.media_object_authority_projection(tenant_id, object_kind);

CREATE INDEX IF NOT EXISTS idx_media_object_authority_artifact_hash
    ON foghorn.media_object_authority_projection(artifact_hash)
    WHERE artifact_hash IS NOT NULL;

CREATE UNIQUE INDEX IF NOT EXISTS uq_media_object_authority_publishing_credential
    ON foghorn.media_object_authority_projection(publishing_credential_sha256)
    WHERE publishing_credential_sha256 IS NOT NULL AND lifecycle = 'active';

CREATE TABLE IF NOT EXISTS foghorn.media_authority_apply_audit (
    id BIGSERIAL PRIMARY KEY,
    authority_kind VARCHAR(32),
    authority_id VARCHAR(255),
    authority_version BIGINT,
    signer_key_id VARCHAR(255),
    payload_sha256 BYTEA,
    outcome VARCHAR(32) NOT NULL,
    reason TEXT NOT NULL DEFAULT '',
    observed_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CONSTRAINT chk_media_authority_apply_outcome
        CHECK (outcome IN ('applied', 'duplicate', 'rollback_rejected', 'conflict_rejected', 'verification_rejected'))
);

CREATE INDEX IF NOT EXISTS idx_media_authority_apply_audit_authority
    ON foghorn.media_authority_apply_audit(authority_kind, authority_id, observed_at DESC);

CREATE INDEX IF NOT EXISTS idx_media_authority_apply_audit_retention
    ON foghorn.media_authority_apply_audit(observed_at);

-- Bind each admitted publisher generation to the exact signed authority and live
-- process policy that authorized it. These are nullable for pre-v0.3.0 sessions;
-- every locally-authorized outage admission writes positive versions.
ALTER TABLE foghorn.ingest_sessions
    ADD COLUMN IF NOT EXISTS media_authority_id VARCHAR(255),
    ADD COLUMN IF NOT EXISTS media_authority_version BIGINT,
    ADD COLUMN IF NOT EXISTS tenant_authority_version BIGINT,
    ADD COLUMN IF NOT EXISTS processes_json TEXT NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS capacity_max_streams INTEGER NOT NULL DEFAULT 0;

DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM pg_constraint
        WHERE conrelid = 'foghorn.ingest_sessions'::regclass
          AND conname = 'ck_foghorn_ingest_sessions_media_authority_version'
    ) THEN
        ALTER TABLE foghorn.ingest_sessions
            ADD CONSTRAINT ck_foghorn_ingest_sessions_media_authority_version
            CHECK (media_authority_version IS NULL OR media_authority_version > 0) NOT VALID;
    END IF;
    IF NOT EXISTS (
        SELECT 1 FROM pg_constraint
        WHERE conrelid = 'foghorn.ingest_sessions'::regclass
          AND conname = 'ck_foghorn_ingest_sessions_tenant_authority_version'
    ) THEN
        ALTER TABLE foghorn.ingest_sessions
            ADD CONSTRAINT ck_foghorn_ingest_sessions_tenant_authority_version
            CHECK (tenant_authority_version IS NULL OR tenant_authority_version > 0) NOT VALID;
    END IF;
    IF NOT EXISTS (
        SELECT 1 FROM pg_constraint
        WHERE conrelid = 'foghorn.ingest_sessions'::regclass
          AND conname = 'ck_foghorn_ingest_sessions_media_authority_identity'
    ) THEN
        ALTER TABLE foghorn.ingest_sessions
            ADD CONSTRAINT ck_foghorn_ingest_sessions_media_authority_identity
            CHECK ((media_authority_version IS NULL) = (media_authority_id IS NULL)) NOT VALID;
    END IF;
    IF NOT EXISTS (
        SELECT 1 FROM pg_constraint
        WHERE conrelid = 'foghorn.ingest_sessions'::regclass
          AND conname = 'ck_foghorn_ingest_sessions_capacity_max_streams'
    ) THEN
        ALTER TABLE foghorn.ingest_sessions
            ADD CONSTRAINT ck_foghorn_ingest_sessions_capacity_max_streams
            CHECK (capacity_max_streams >= 0) NOT VALID;
    END IF;
END
$$;

-- An applied multistream activation remains the exact authority for its active publisher
-- generation. A newer authenticated Helmsman connection advances this fence and re-arms the
-- activation; acknowledgements from the retired connection cannot settle the replay.
ALTER TABLE foghorn.ingest_admission_effects
    ADD COLUMN IF NOT EXISTS activation_connection_fence BIGINT NOT NULL DEFAULT 0;

-- v2 states fence encrypted activation payloads from pre-v0.3 workers during
-- a rolling Foghorn upgrade. Old workers query only pending/applied and safely
-- ignore the new family; v0.3 workers can drain both families.
ALTER TABLE foghorn.ingest_admission_effects
    DROP CONSTRAINT IF EXISTS ingest_admission_effects_state_check;

DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM pg_constraint
        WHERE conrelid = 'foghorn.ingest_admission_effects'::regclass
          AND conname = 'ck_foghorn_ingest_admission_state'
    ) THEN
        ALTER TABLE foghorn.ingest_admission_effects
            ADD CONSTRAINT ck_foghorn_ingest_admission_state
            CHECK (state IN ('pending', 'applied', 'superseded',
                             'pending_v2', 'applied_v2', 'superseded_v2')) NOT VALID;
    END IF;
END
$$;

DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM pg_constraint
        WHERE conrelid = 'foghorn.ingest_admission_effects'::regclass
          AND conname = 'ck_foghorn_ingest_admission_activation_fence'
    ) THEN
        ALTER TABLE foghorn.ingest_admission_effects
            ADD CONSTRAINT ck_foghorn_ingest_admission_activation_fence
            CHECK (activation_connection_fence >= 0) NOT VALID;
    END IF;
END
$$;

CREATE TABLE IF NOT EXISTS foghorn.node_config_seeds (
    node_id          VARCHAR(100) PRIMARY KEY,
    version_counter  BIGINT NOT NULL DEFAULT 0 CHECK (version_counter >= 0),
    seed_version     BIGINT CHECK (seed_version IS NULL OR seed_version > 0),
    seed_payload     BYTEA,
    updated_at       TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CONSTRAINT ck_foghorn_node_config_seed_pair
        CHECK ((seed_version IS NULL) = (seed_payload IS NULL))
);

CREATE TABLE IF NOT EXISTS foghorn.push_target_status_outbox (
    id BIGSERIAL PRIMARY KEY,
    target_id UUID NOT NULL UNIQUE,
    tenant_id UUID NOT NULL,
    status VARCHAR(20) NOT NULL,
    last_error TEXT,
    event_unix_millis BIGINT NOT NULL DEFAULT 0 CHECK (event_unix_millis >= 0),
    revision BIGINT NOT NULL DEFAULT 1 CHECK (revision > 0),
    attempts INTEGER NOT NULL DEFAULT 0 CHECK (attempts >= 0),
    next_attempt_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    last_attempt_at TIMESTAMPTZ,
    lease_owner VARCHAR(100),
    lease_until TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CONSTRAINT ck_foghorn_push_target_status_outbox_status
        CHECK (status IN ('idle', 'pushing', 'failed'))
);

ALTER TABLE foghorn.push_target_status_outbox
    ADD COLUMN IF NOT EXISTS lease_owner VARCHAR(100),
    ADD COLUMN IF NOT EXISTS lease_until TIMESTAMPTZ,
    ADD COLUMN IF NOT EXISTS event_unix_millis BIGINT NOT NULL DEFAULT 0;

CREATE INDEX IF NOT EXISTS idx_foghorn_push_target_status_outbox_due
    ON foghorn.push_target_status_outbox(next_attempt_at, id);

CREATE TABLE IF NOT EXISTS foghorn.managed_stream_placement_outbox (
    id BIGSERIAL PRIMARY KEY,
    stream_id UUID NOT NULL UNIQUE,
    tenant_id UUID NOT NULL,
    cluster_id UUID NOT NULL,
    desired_active BOOLEAN NOT NULL,
    revision BIGINT NOT NULL DEFAULT 1 CHECK (revision > 0),
    attempts INTEGER NOT NULL DEFAULT 0 CHECK (attempts >= 0),
    next_attempt_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    last_attempt_at TIMESTAMPTZ,
    lease_owner VARCHAR(100),
    lease_until TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

ALTER TABLE foghorn.managed_stream_placement_outbox
    ADD COLUMN IF NOT EXISTS lease_owner VARCHAR(100),
    ADD COLUMN IF NOT EXISTS lease_until TIMESTAMPTZ;

CREATE INDEX IF NOT EXISTS idx_foghorn_managed_stream_placement_outbox_due
    ON foghorn.managed_stream_placement_outbox(next_attempt_at, id);

CREATE TABLE IF NOT EXISTS foghorn.signing_key_use_outbox (
    id BIGSERIAL PRIMARY KEY,
    tenant_id UUID NOT NULL,
    kid VARCHAR(255) NOT NULL,
    revision BIGINT NOT NULL DEFAULT 1 CHECK (revision > 0),
    attempts INTEGER NOT NULL DEFAULT 0 CHECK (attempts >= 0),
    next_attempt_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    last_attempt_at TIMESTAMPTZ,
    lease_owner VARCHAR(100),
    lease_until TIMESTAMPTZ,
    observed_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE (tenant_id, kid)
);

CREATE INDEX IF NOT EXISTS idx_foghorn_signing_key_use_outbox_due
    ON foghorn.signing_key_use_outbox(next_attempt_at, id);

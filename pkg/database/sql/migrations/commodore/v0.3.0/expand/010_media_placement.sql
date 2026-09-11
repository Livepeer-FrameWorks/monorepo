-- Scoped media placement intent and durable apply receipts.
CREATE TABLE IF NOT EXISTS commodore.media_placement_policies (
    tenant_id UUID NOT NULL,
    scope_kind VARCHAR(16) NOT NULL,
    scope_id UUID NOT NULL,
    revision BIGINT NOT NULL DEFAULT 0 CHECK (revision >= 0),
    parent_revision BIGINT NOT NULL DEFAULT 0 CHECK (parent_revision >= 0),
    policy_payload BYTEA NOT NULL DEFAULT '\x'::bytea CHECK (octet_length(policy_payload) <= 1048576),
    active_revision BIGINT NOT NULL DEFAULT 0 CHECK (active_revision >= 0 AND active_revision <= revision),
    active_parent_revision BIGINT NOT NULL DEFAULT 0 CHECK (active_parent_revision >= 0),
    active_policy_payload BYTEA NOT NULL DEFAULT '\x'::bytea CHECK (octet_length(active_policy_payload) <= 1048576),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (tenant_id, scope_kind, scope_id),
    CONSTRAINT chk_media_placement_scope
        CHECK (scope_kind IN ('tenant', 'stream') AND (scope_kind <> 'tenant' OR scope_id = tenant_id))
);

CREATE TABLE IF NOT EXISTS commodore.media_placement_changes (
    tenant_id UUID NOT NULL,
    scope_kind VARCHAR(16) NOT NULL,
    scope_id UUID NOT NULL,
    idempotency_key VARCHAR(128) NOT NULL CHECK (btrim(idempotency_key) <> ''),
    request_sha256 BYTEA NOT NULL CHECK (octet_length(request_sha256) = 32),
    revision BIGINT NOT NULL CHECK (revision > 0),
    parent_revision BIGINT NOT NULL CHECK (parent_revision >= 0),
    policy_digest VARCHAR(64) NOT NULL CHECK (length(policy_digest) = 64),
    review_digest VARCHAR(64) NOT NULL CHECK (review_digest ~ '^[0-9a-f]{64}$'),
    previous_policy_payload BYTEA NOT NULL CHECK (octet_length(previous_policy_payload) <= 1048576),
    policy_payload BYTEA NOT NULL CHECK (octet_length(policy_payload) BETWEEN 1 AND 1048576),
    actor_id VARCHAR(255) NOT NULL CHECK (btrim(actor_id) <> ''),
    rollout_status VARCHAR(16) NOT NULL DEFAULT 'pending',
    rollout_reason TEXT NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (tenant_id, scope_kind, scope_id, idempotency_key),
    UNIQUE (tenant_id, scope_kind, scope_id, revision),
    CONSTRAINT fk_media_placement_change_scope
        FOREIGN KEY (tenant_id, scope_kind, scope_id)
        REFERENCES commodore.media_placement_policies(tenant_id, scope_kind, scope_id),
    CONSTRAINT chk_media_placement_rollout_status
        CHECK (rollout_status IN ('pending', 'effective', 'blocked', 'superseded'))
);

CREATE INDEX IF NOT EXISTS idx_media_placement_changes_pending
    ON commodore.media_placement_changes(tenant_id, created_at)
    WHERE rollout_status IN ('pending', 'blocked');

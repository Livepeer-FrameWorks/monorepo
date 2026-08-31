CREATE TABLE IF NOT EXISTS navigator.tenant_alias_cluster_authority (
    tenant_id UUID NOT NULL
        REFERENCES navigator.tenant_aliases(tenant_id) ON DELETE CASCADE,
    cluster_id TEXT NOT NULL,
    state TEXT NOT NULL
        CONSTRAINT ck_navigator_tenant_alias_cluster_authority_state
        CHECK (state IN ('active', 'revoked')),
    authority_sequence BIGINT NOT NULL DEFAULT 0
        CONSTRAINT ck_navigator_tenant_alias_cluster_authority_sequence
        CHECK (authority_sequence >= 0),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (tenant_id, cluster_id)
);

-- Existing ACK rows (including pending_apply failures) prove that the cluster
-- received a tenant bundle under the legacy Quartermaster admission path; they
-- do not prove successful certificate activation. Seed that admission as a
-- sequence-zero active decision. DNS readiness still requires applied/in_dns,
-- and any ordered revocation wins at the same or a greater sequence.
INSERT INTO navigator.tenant_alias_cluster_authority (
    tenant_id, cluster_id, state, authority_sequence
)
SELECT DISTINCT edge.tenant_id, edge.cluster_id, 'active', 0
FROM navigator.tenant_edge_apply_state AS edge
WHERE EXISTS (
    SELECT 1
    FROM navigator.tenant_aliases AS alias
    WHERE alias.tenant_id = edge.tenant_id
)
ON CONFLICT (tenant_id, cluster_id) DO NOTHING;

CREATE TABLE IF NOT EXISTS quartermaster.media_authority_refresh_outbox (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    source_event_id VARCHAR(255) NOT NULL UNIQUE,
    tenant_id UUID NOT NULL,
    reason VARCHAR(255) NOT NULL,
    status VARCHAR(20) NOT NULL DEFAULT 'pending',
    attempts INTEGER NOT NULL DEFAULT 0 CHECK (attempts >= 0),
    next_attempt_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    lease_expires_at TIMESTAMPTZ,
    completed_at TIMESTAMPTZ,
    last_error TEXT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CONSTRAINT chk_quartermaster_media_authority_refresh_status
        CHECK (status IN ('pending', 'delivering', 'completed')),
    CONSTRAINT chk_quartermaster_media_authority_refresh_reason
        CHECK (btrim(reason) <> '')
);

CREATE INDEX IF NOT EXISTS idx_quartermaster_media_authority_refresh_pending
    ON quartermaster.media_authority_refresh_outbox(next_attempt_at, created_at)
    WHERE status <> 'completed';

CREATE INDEX IF NOT EXISTS idx_quartermaster_media_authority_refresh_tenant
    ON quartermaster.media_authority_refresh_outbox(tenant_id, created_at DESC);

CREATE OR REPLACE FUNCTION quartermaster.enqueue_media_authority_refresh(
    p_tenant_id UUID,
    p_reason TEXT
) RETURNS VOID
LANGUAGE plpgsql
AS $$
BEGIN
    IF p_tenant_id IS NULL THEN
        RETURN;
    END IF;
    INSERT INTO quartermaster.media_authority_refresh_outbox(source_event_id, tenant_id, reason)
    VALUES (p_reason || ':' || gen_random_uuid()::text, p_tenant_id, p_reason);
END;
$$;

CREATE OR REPLACE FUNCTION quartermaster.media_authority_tenant_changed()
RETURNS TRIGGER
LANGUAGE plpgsql
AS $$
DECLARE
    affected_tenant UUID;
BEGIN
    affected_tenant := CASE WHEN TG_OP = 'DELETE' THEN OLD.id ELSE NEW.id END;
    PERFORM quartermaster.enqueue_media_authority_refresh(affected_tenant, 'tenant_authority_changed');
    RETURN CASE WHEN TG_OP = 'DELETE' THEN OLD ELSE NEW END;
END;
$$;

CREATE OR REPLACE TRIGGER trg_tenant_media_authority
AFTER INSERT OR DELETE OR UPDATE OF deployment_tier, deployment_model, primary_cluster_id,
    official_cluster_id, is_active, rate_limit_per_minute, rate_limit_burst
ON quartermaster.tenants
FOR EACH ROW EXECUTE FUNCTION quartermaster.media_authority_tenant_changed();

CREATE OR REPLACE FUNCTION quartermaster.media_authority_cluster_access_changed()
RETURNS TRIGGER
LANGUAGE plpgsql
AS $$
DECLARE
    affected_tenant UUID;
BEGIN
    affected_tenant := CASE WHEN TG_OP = 'DELETE' THEN OLD.tenant_id ELSE NEW.tenant_id END;
    PERFORM quartermaster.enqueue_media_authority_refresh(affected_tenant, 'cluster_access_changed');
    RETURN CASE WHEN TG_OP = 'DELETE' THEN OLD ELSE NEW END;
END;
$$;

CREATE OR REPLACE TRIGGER trg_cluster_access_media_authority
AFTER INSERT OR UPDATE OR DELETE
ON quartermaster.tenant_cluster_access
FOR EACH ROW EXECUTE FUNCTION quartermaster.media_authority_cluster_access_changed();

CREATE OR REPLACE FUNCTION quartermaster.media_authority_cluster_changed()
RETURNS TRIGGER
LANGUAGE plpgsql
AS $$
DECLARE
    old_cluster TEXT;
    new_cluster TEXT;
BEGIN
    old_cluster := CASE WHEN TG_OP IN ('UPDATE', 'DELETE') THEN OLD.cluster_id ELSE NULL END;
    new_cluster := CASE WHEN TG_OP IN ('INSERT', 'UPDATE') THEN NEW.cluster_id ELSE NULL END;
    INSERT INTO quartermaster.media_authority_refresh_outbox(source_event_id, tenant_id, reason)
    SELECT 'cluster_authority_changed:' || gen_random_uuid()::text, access.tenant_id,
           'cluster_authority_changed'
    FROM quartermaster.tenant_cluster_access AS access
    WHERE access.cluster_id IN (old_cluster, new_cluster);
    RETURN CASE WHEN TG_OP = 'DELETE' THEN OLD ELSE NEW END;
END;
$$;

CREATE OR REPLACE TRIGGER trg_infrastructure_cluster_media_authority
AFTER INSERT OR DELETE OR UPDATE OF deployment_model, owner_tenant_id, cluster_class,
    cell_id, control_cell_id, eligible_serving_cell_ids, allow_private_pull_sources, is_active
ON quartermaster.infrastructure_clusters
FOR EACH ROW EXECUTE FUNCTION quartermaster.media_authority_cluster_changed();

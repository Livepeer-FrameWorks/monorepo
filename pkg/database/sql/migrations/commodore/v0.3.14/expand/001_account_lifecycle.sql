-- Private operator explanations are isolated from tenant-visible domain events.
CREATE TABLE IF NOT EXISTS commodore.operator_account_audit (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id UUID NOT NULL,
    actor_user_id TEXT NOT NULL,
    target_id TEXT NOT NULL,
    action TEXT NOT NULL,
    reason TEXT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX IF NOT EXISTS idx_operator_account_audit_tenant_time
ON commodore.operator_account_audit(tenant_id, created_at ASC, id ASC);

-- One tenant guard serializes account closure with user activation and creation.
-- Closure retry state persists independently of Quartermaster availability.
CREATE TABLE IF NOT EXISTS commodore.tenant_account_states (
    tenant_id UUID PRIMARY KEY,
    closed_at TIMESTAMPTZ,
    reason TEXT NOT NULL DEFAULT '',
    closed_by TEXT NOT NULL DEFAULT '',
    quartermaster_closed_at TIMESTAMPTZ,
    closure_attempted_at TIMESTAMPTZ,
    closure_retry_after TIMESTAMPTZ,
    closure_error TEXT,
    closure_failures INTEGER NOT NULL DEFAULT 0
);
CREATE OR REPLACE FUNCTION commodore.guard_active_tenant_user()
RETURNS TRIGGER LANGUAGE plpgsql AS $$
DECLARE closed TIMESTAMPTZ;
BEGIN
    -- Hold the tenant guard row through commit so closure and user writes serialize.
    INSERT INTO commodore.tenant_account_states(tenant_id)
    VALUES(NEW.tenant_id) ON CONFLICT (tenant_id) DO NOTHING;
    SELECT closed_at INTO closed FROM commodore.tenant_account_states
    WHERE tenant_id=NEW.tenant_id FOR UPDATE;
    IF COALESCE(NEW.is_active, false) AND closed IS NOT NULL THEN
        RAISE EXCEPTION 'tenant account is permanently closed' USING ERRCODE = '23514', CONSTRAINT = 'tenant_account_closed';
    END IF;
    RETURN NEW;
END;
$$;
CREATE OR REPLACE TRIGGER trg_guard_active_tenant_user
BEFORE INSERT OR UPDATE OF is_active, tenant_id ON commodore.users
FOR EACH ROW EXECUTE FUNCTION commodore.guard_active_tenant_user();

CREATE INDEX IF NOT EXISTS idx_tenant_account_states_closures_pending
ON commodore.tenant_account_states(closure_attempted_at ASC NULLS FIRST, closed_at ASC, tenant_id)
WHERE closed_at IS NOT NULL AND quartermaster_closed_at IS NULL;

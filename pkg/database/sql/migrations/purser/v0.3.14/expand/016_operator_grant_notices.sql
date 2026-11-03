-- Emails telling a tenant its operator grant ends soon ('ending', seven days
-- before expires_at) or has ended ('ended'). One row per subscription, kind
-- and end time, so an extended grant is announced again for its new end.
CREATE TABLE IF NOT EXISTS purser.operator_grant_notices (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id UUID NOT NULL,
    subscription_id UUID NOT NULL REFERENCES purser.tenant_subscriptions(id) ON DELETE CASCADE,
    kind VARCHAR(16) NOT NULL,
    ends_at TIMESTAMPTZ NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    attempted_at TIMESTAMPTZ,
    failures INTEGER NOT NULL DEFAULT 0,
    error TEXT,
    notified_at TIMESTAMPTZ,
    abandoned_at TIMESTAMPTZ,
    CONSTRAINT chk_operator_grant_notice_kind CHECK (kind IN ('ending', 'ended')),
    CONSTRAINT uq_operator_grant_notice UNIQUE (subscription_id, kind, ends_at)
);

CREATE INDEX IF NOT EXISTS idx_operator_grant_notices_pending
ON purser.operator_grant_notices(attempted_at ASC, ends_at)
WHERE notified_at IS NULL AND abandoned_at IS NULL;

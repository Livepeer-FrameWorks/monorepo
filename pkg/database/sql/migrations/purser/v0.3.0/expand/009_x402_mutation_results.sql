-- A paid authorization may be retried after settlement succeeds. Keep the
-- application mutation result independently durable so the underlying
-- side-effect is never executed twice merely because the HTTP/MCP reply was
-- lost. Ambiguous rows move to operator_review and can receive a known result;
-- they are never released for blind re-execution.

CREATE TABLE IF NOT EXISTS purser.x402_mutation_results (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id UUID NOT NULL,
    quote_id UUID NOT NULL REFERENCES purser.x402_payment_quotes(id),
    idempotency_key VARCHAR(255) NOT NULL,
    request_fingerprint VARCHAR(64) NOT NULL CHECK (request_fingerprint ~ '^[0-9a-f]{64}$'),
    protocol VARCHAR(12) NOT NULL CHECK (protocol IN ('http', 'mcp')),
    operation VARCHAR(255) NOT NULL,
	status VARCHAR(24) NOT NULL DEFAULT 'claimed' CHECK (status IN ('claimed', 'completion_pending', 'completed', 'operator_review')),
	result BYTEA,
	content_type VARCHAR(255),
	status_code INTEGER,
	attempt_count INTEGER NOT NULL DEFAULT 1 CHECK (attempt_count > 0),
	last_attempt_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
	review_reason TEXT,
	resolved_by UUID,
	resolved_at TIMESTAMPTZ,
    claimed_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    completed_at TIMESTAMPTZ,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE(tenant_id, idempotency_key),
    UNIQUE(quote_id)
);

CREATE INDEX IF NOT EXISTS idx_x402_mutation_results_unknown
    ON purser.x402_mutation_results(claimed_at)
	WHERE status IN ('claimed', 'completion_pending', 'operator_review');

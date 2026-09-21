-- What a card provider actually settled for a charge: the charge amount and
-- currency, the EUR amount credited to the platform balance, the provider fee,
-- the net, the provider's exchange rate, and the provider balance transaction.
-- A pending row has no settlement figures yet.
CREATE TABLE IF NOT EXISTS purser.provider_settlements (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id UUID NOT NULL,
    provider VARCHAR(20) NOT NULL,
    provider_payment_id VARCHAR(255) NOT NULL,
    provider_balance_transaction_id VARCHAR(255),
    pending_topup_id UUID REFERENCES purser.pending_topups(id) ON DELETE SET NULL,
    payment_id UUID REFERENCES purser.billing_payments(id) ON DELETE SET NULL,
    charge_amount_cents BIGINT NOT NULL,
    charge_currency CHAR(3) NOT NULL,
    settled_amount_cents BIGINT,
    fee_cents BIGINT,
    net_cents BIGINT,
    settlement_currency CHAR(3),
    exchange_rate NUMERIC(20, 10),
    status VARCHAR(16) NOT NULL DEFAULT 'pending',
    settled_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE (provider, provider_payment_id),
    CONSTRAINT chk_provider_settlements_provider CHECK (provider IN ('stripe', 'mollie')),
    CONSTRAINT chk_provider_settlements_charge CHECK (
        charge_amount_cents > 0 AND charge_currency IN ('EUR', 'USD', 'GBP')
    ),
    CONSTRAINT chk_provider_settlements_state CHECK (
        (status = 'pending'
            AND provider_balance_transaction_id IS NULL AND settled_amount_cents IS NULL
            AND fee_cents IS NULL AND net_cents IS NULL AND settlement_currency IS NULL
            AND exchange_rate IS NULL AND settled_at IS NULL)
        OR (status = 'settled'
            AND provider_balance_transaction_id IS NOT NULL AND settled_amount_cents IS NOT NULL
            AND fee_cents IS NOT NULL AND fee_cents >= 0
            AND net_cents IS NOT NULL AND net_cents = settled_amount_cents - fee_cents
            AND settlement_currency IS NOT NULL AND settlement_currency = 'EUR' AND settled_at IS NOT NULL
            AND ((exchange_rate IS NOT NULL AND exchange_rate > 0)
                 OR (exchange_rate IS NULL AND charge_currency = 'EUR')))
    )
);

CREATE UNIQUE INDEX IF NOT EXISTS uq_provider_settlements_balance_transaction
    ON purser.provider_settlements(provider, provider_balance_transaction_id)
    WHERE provider_balance_transaction_id IS NOT NULL;
CREATE INDEX IF NOT EXISTS idx_provider_settlements_pending
    ON purser.provider_settlements(provider, updated_at)
    WHERE status = 'pending';
CREATE INDEX IF NOT EXISTS idx_provider_settlements_tenant
    ON purser.provider_settlements(tenant_id, created_at DESC);

-- Position of the Mollie balance transaction reader per Mollie balance.
CREATE TABLE IF NOT EXISTS purser.mollie_balance_cursors (
    balance_id VARCHAR(64) PRIMARY KEY,
    last_transaction_id VARCHAR(64),
    last_transaction_created_at TIMESTAMPTZ,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

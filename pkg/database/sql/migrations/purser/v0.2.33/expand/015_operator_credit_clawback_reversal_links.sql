CREATE TABLE IF NOT EXISTS purser.operator_credit_clawback_reversals (
    payment_reversal_id UUID NOT NULL REFERENCES purser.payment_reversals(id) ON DELETE CASCADE,
    operator_credit_ledger_id UUID NOT NULL REFERENCES purser.operator_credit_ledger(id) ON DELETE CASCADE,
    accrual_ledger_id UUID NOT NULL REFERENCES purser.operator_credit_ledger(id) ON DELETE CASCADE,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (payment_reversal_id, accrual_ledger_id),
    UNIQUE (operator_credit_ledger_id)
);

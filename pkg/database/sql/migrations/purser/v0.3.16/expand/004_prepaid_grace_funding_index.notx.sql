CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_prepaid_grace_settled_topup ON purser.balance_transactions(tenant_id,amount_cents DESC) WHERE transaction_type='topup' AND amount_cents>0;

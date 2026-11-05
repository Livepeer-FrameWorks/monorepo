CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_billing_payments_tenant_id_uuid ON purser.billing_payments(tenant_id,id);

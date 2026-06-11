-- ============================================================================
-- OPERATOR-ANALYTICS READ-ONLY ROLE - purser grants
-- ============================================================================
-- See analytics_ro_quartermaster.sql for the role contract. Fail-closed
-- allowlist: payment-provider objects, wallet state, webhook payloads, and
-- KYC records are never granted.
-- ============================================================================

DO $$
BEGIN
    IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'frameworks_analytics_ro') THEN
        CREATE ROLE frameworks_analytics_ro LOGIN;
    END IF;
END
$$;

GRANT USAGE ON SCHEMA purser TO frameworks_analytics_ro;

GRANT SELECT ON
    purser.billing_tiers,
    purser.tier_entitlements,
    purser.tier_pricing_rules,
    purser.billing_invoices,
    purser.invoice_line_items,
    purser.usage_records,
    purser.usage_adjustments,
    purser.prepaid_balances,
    purser.balance_transactions,
    purser.tenant_balance_rollups,
    purser.cluster_pricing,
    purser.cluster_pricing_history,
    purser.cluster_subscriptions,
    purser.operator_credit_ledger,
    purser.operator_payouts,
    purser.platform_fee_policy,
    purser.simplified_invoices,
    purser.storage_provider_usage_records,
    purser.subscription_pricing_overrides,
    purser.subscription_entitlement_overrides
TO frameworks_analytics_ro;

-- Billing PII (email, address, tax id) and payment-provider references
-- stay private; subscription lifecycle is what analytics wants.
GRANT SELECT (
    id, tenant_id, tier_id, status, billing_model,
    started_at, trial_ends_at, next_billing_date,
    billing_period_start, billing_period_end, cancelled_at,
    pending_tier_id, pending_effective_at, pending_reason,
    created_at, updated_at
) ON purser.tenant_subscriptions TO frameworks_analytics_ro;

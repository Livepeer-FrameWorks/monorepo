-- Complete the v0.3 vocabulary switch only after the rollback window closes.

UPDATE purser.tier_pricing_rules r
SET model = 'dimensioned',
    config = (r.config - 'codec_multipliers') || jsonb_build_object(
        'rated_quantity_divisor', 60,
        'rates', COALESCE((
            SELECT jsonb_agg(jsonb_build_object(
                'selectors', jsonb_build_object('output_codec', lower(CASE WHEN position(':' IN k.key) > 0 THEN split_part(k.key, ':', 2) ELSE k.key END)),
                'unit_price', r.unit_price * (k.value::text)::numeric
            ))
            FROM jsonb_each(COALESCE(r.config->'codec_multipliers', '{}'::jsonb)) k
        ), '[]'::jsonb)
    )
WHERE model = 'codec_multiplier';

UPDATE purser.subscription_pricing_overrides
SET model = 'dimensioned'
WHERE model = 'codec_multiplier';

ALTER TABLE purser.tier_pricing_rules
    DROP CONSTRAINT IF EXISTS chk_tier_pricing_model_v3_compat;
ALTER TABLE purser.tier_pricing_rules
    DROP CONSTRAINT IF EXISTS chk_tier_pricing_model;
ALTER TABLE purser.tier_pricing_rules
    ADD CONSTRAINT chk_tier_pricing_model
    CHECK (model IN ('tiered_graduated', 'all_usage', 'dimensioned'));
ALTER TABLE purser.subscription_pricing_overrides
    DROP CONSTRAINT IF EXISTS chk_subscription_pricing_model_v3_compat;
ALTER TABLE purser.subscription_pricing_overrides
    DROP CONSTRAINT IF EXISTS chk_subscription_pricing_model;
ALTER TABLE purser.subscription_pricing_overrides
    ADD CONSTRAINT chk_subscription_pricing_model
    CHECK (model IS NULL OR model IN ('tiered_graduated', 'all_usage', 'dimensioned'));

ALTER TABLE purser.usage_records
    ALTER COLUMN unit DROP DEFAULT,
    ALTER COLUMN dimension_key DROP DEFAULT,
    ALTER COLUMN source_id DROP DEFAULT,
    ALTER COLUMN report_id DROP DEFAULT;
DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1
        FROM pg_constraint
        WHERE conrelid = 'purser.usage_records'::regclass
          AND conname = 'usage_records_dimensioned_window_key'
    ) THEN
        ALTER TABLE purser.usage_records
            ADD CONSTRAINT usage_records_dimensioned_window_key
            UNIQUE USING INDEX usage_records_dimensioned_window_key;
    END IF;
END $$;

UPDATE purser.operator_credit_ledger
SET source_type = 'provider_usage'
WHERE source_type = 'storage_provider_usage';
ALTER TABLE purser.operator_credit_ledger
    DROP CONSTRAINT IF EXISTS chk_op_credit_source_v3_compat;
ALTER TABLE purser.operator_credit_ledger
    DROP CONSTRAINT IF EXISTS chk_op_credit_source;
ALTER TABLE purser.operator_credit_ledger
    ADD CONSTRAINT chk_op_credit_source CHECK (
        (source_type = 'invoice_line'        AND invoice_line_item_id IS NOT NULL) OR
        (source_type = 'provider_usage'      AND provider_usage_record_id IS NOT NULL) OR
        (source_type = 'usage_adjustment'    AND usage_adjustment_id IS NOT NULL) OR
        (source_type = 'stripe_subscription' AND stripe_invoice_id IS NOT NULL)
    );

DROP INDEX IF EXISTS purser.uq_op_credit_accrual_storage_provider_usage;
DROP VIEW IF EXISTS purser.payment_report_operator_credits_without_clawback;
ALTER TABLE purser.operator_credit_ledger
    DROP COLUMN IF EXISTS storage_provider_usage_record_id;
DROP TABLE IF EXISTS purser.storage_provider_usage_records;

CREATE VIEW purser.payment_report_operator_credits_without_clawback AS
SELECT
    accrual.id,
    accrual.source_type,
    accrual.invoice_line_item_id,
    accrual.provider_usage_record_id,
    accrual.usage_adjustment_id,
    accrual.stripe_invoice_id,
    accrual.entry_type,
    accrual.reverses_ledger_id,
    accrual.cluster_owner_tenant_id,
    accrual.cluster_id,
    accrual.invoice_id,
    accrual.period_start,
    accrual.period_end,
    accrual.currency,
    accrual.gross_cents,
    accrual.platform_fee_cents,
    accrual.payable_cents,
    accrual.status,
    accrual.payout_batch_id,
    accrual.notes,
    accrual.created_at,
    accrual.updated_at
FROM purser.operator_credit_ledger accrual
JOIN purser.payment_reversals pr
    ON pr.invoice_id = accrual.invoice_id
   AND pr.status = 'succeeded'
LEFT JOIN purser.operator_credit_ledger clawback
    ON clawback.reverses_ledger_id = accrual.id
   AND clawback.entry_type = 'clawback'
WHERE accrual.entry_type = 'accrual'
  AND clawback.id IS NULL;

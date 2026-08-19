-- Populate additive v0.3 columns after all writers understand them. This phase
-- remains rollback-safe: v0.2 tables, columns, values, and constraints stay.

UPDATE purser.usage_records
SET dimension_key = encode(digest(dimensions::text, 'sha256'), 'hex')
WHERE dimension_key = repeat('0', 64);

DO $$
BEGIN
    IF to_regclass('purser.storage_provider_usage_records') IS NOT NULL THEN
        INSERT INTO purser.provider_usage_records (
            id, usage_tenant_id, work_cluster_id,
            provider_tenant_id, provider_cluster_id,
            usage_type, unit, usage_value, dimensions, dimension_key,
            source_id, report_id, period_start, period_end,
            granularity, value_kind, source, usage_details, created_at, updated_at
        )
        SELECT id, usage_tenant_id, customer_cluster_id,
               storage_provider_tenant_id, storage_provider_cluster_id,
               usage_type, 'gibibyte_second', gb_seconds,
               jsonb_build_object('storage_backend', storage_backend, 'storage_scope', storage_scope),
               encode(digest(jsonb_build_object('storage_backend', storage_backend, 'storage_scope', storage_scope)::text, 'sha256'), 'hex'),
               'legacy', repeat('0', 64), period_start, period_end,
               granularity, value_kind, source, usage_details, created_at, updated_at
        FROM purser.storage_provider_usage_records
        ON CONFLICT DO NOTHING;
    END IF;

    IF EXISTS (
        SELECT 1
        FROM information_schema.columns
        WHERE table_schema = 'purser'
          AND table_name = 'operator_credit_ledger'
          AND column_name = 'storage_provider_usage_record_id'
    ) THEN
        UPDATE purser.operator_credit_ledger
        SET provider_usage_record_id = storage_provider_usage_record_id
        WHERE source_type = 'storage_provider_usage'
          AND provider_usage_record_id IS NULL;
    END IF;
END $$;

ALTER TABLE purser.usage_reports
    VALIDATE CONSTRAINT chk_usage_reports_kind;

-- v0.3.0: install the additive dimensioned-metering schema. Compatibility
-- constraints accept both v0.2 and v0.3 writers during the rolling upgrade;
-- the irreversible vocabulary switch is contract-phase.

CREATE TABLE IF NOT EXISTS purser.meter_definitions (
    meter VARCHAR(64) PRIMARY KEY,
    unit VARCHAR(32) NOT NULL,
    aggregation VARCHAR(16) NOT NULL DEFAULT 'sum',
    display_name VARCHAR(100) NOT NULL,
    allowed_dimensions TEXT[] NOT NULL DEFAULT '{}',
    default_priceable BOOLEAN NOT NULL DEFAULT FALSE,
    active BOOLEAN NOT NULL DEFAULT TRUE,
    CONSTRAINT chk_meter_definition_name CHECK (meter ~ '^[a-z][a-z0-9_]{0,63}$'),
    CONSTRAINT chk_meter_definition_aggregation CHECK (aggregation IN ('sum', 'max', 'last'))
);

INSERT INTO purser.meter_definitions
    (meter, unit, aggregation, display_name, allowed_dimensions, default_priceable)
VALUES
    ('delivered_minutes', 'minute', 'sum', 'Delivered minutes', '{}', TRUE),
    ('ingress_gb', 'gibibyte', 'sum', 'Ingress bandwidth', '{}', FALSE),
    ('egress_gb', 'gibibyte', 'sum', 'Egress bandwidth', '{}', TRUE),
    ('stream_runtime_seconds', 'second', 'sum', 'Stream runtime', '{}', FALSE),
    ('storage_gb_seconds_hot', 'gibibyte_second', 'sum', 'Hot storage', '{storage_backend,storage_scope}', FALSE),
    ('storage_gb_seconds_cold', 'gibibyte_second', 'sum', 'Cold storage', '{storage_backend,storage_scope}', TRUE),
    ('api_requests', 'request', 'sum', 'API requests', '{auth_type,operation_type,service}', FALSE),
    ('api_errors', 'request', 'sum', 'API errors', '{auth_type,operation_type,service}', FALSE),
    ('api_duration_ms', 'millisecond', 'sum', 'API duration', '{auth_type,operation_type,service}', FALSE),
    ('api_complexity', 'point', 'sum', 'API complexity', '{auth_type,operation_type,service}', FALSE),
    ('llm_input_tokens', 'token', 'sum', 'LLM input tokens', '{model,provider,service}', FALSE),
    ('llm_output_tokens', 'token', 'sum', 'LLM output tokens', '{model,provider,service}', FALSE),
    ('embedding_tokens', 'token', 'sum', 'Embedding tokens', '{model,service}', FALSE),
    ('embedding_requests', 'request', 'sum', 'Embedding requests', '{model,provider,service}', FALSE),
    ('search_requests', 'request', 'sum', 'Search requests', '{provider,service}', FALSE),
    ('transcode_input_seconds', 'second', 'sum', 'Transcode input', '{execution_backend,input_codec,output_codec,track_type,rendition_profile,resolution_class}', FALSE),
    ('transcode_rendition_seconds', 'second', 'sum', 'Transcode renditions', '{execution_backend,input_codec,output_codec,track_type,rendition_profile,resolution_class}', TRUE),
    ('remux_seconds', 'second', 'sum', 'Remux processing', '{execution_backend,output_codec,container}', FALSE),
    ('transcription_seconds', 'second', 'sum', 'Transcription', '{execution_backend,model,language}', FALSE),
    ('inference_frames', 'frame', 'sum', 'Inference frames', '{execution_backend,model}', FALSE),
    ('inference_input_tokens', 'token', 'sum', 'Inference input tokens', '{execution_backend,model}', FALSE),
    ('inference_output_tokens', 'token', 'sum', 'Inference output tokens', '{execution_backend,model}', FALSE),
    ('inference_invocations', 'invocation', 'sum', 'Inference invocations', '{execution_backend,model}', FALSE),
    ('peak_bandwidth_mbps', 'megabit_per_second', 'max', 'Peak bandwidth', '{}', FALSE),
    ('max_viewers', 'viewer', 'max', 'Peak viewers', '{}', FALSE),
    ('total_streams', 'stream', 'sum', 'Streams', '{}', FALSE),
    ('total_viewers', 'viewer', 'sum', 'Viewers', '{}', FALSE),
    ('unique_users', 'user', 'max', 'Unique users', '{}', FALSE),
    ('media_seconds', 'second', 'sum', 'Historical media processing', '{execution_backend,output_codec,track_type}', FALSE)
ON CONFLICT (meter) DO UPDATE SET
    unit = EXCLUDED.unit,
    aggregation = EXCLUDED.aggregation,
    display_name = EXCLUDED.display_name,
    allowed_dimensions = EXCLUDED.allowed_dimensions,
    default_priceable = EXCLUDED.default_priceable,
    active = TRUE;

ALTER TABLE purser.tier_pricing_rules
    ADD CONSTRAINT chk_tier_pricing_model_v3_compat
    CHECK (model IN ('tiered_graduated', 'all_usage', 'codec_multiplier', 'dimensioned')) NOT VALID;
ALTER TABLE purser.tier_pricing_rules
    DROP CONSTRAINT IF EXISTS chk_tier_pricing_model;

ALTER TABLE purser.subscription_pricing_overrides
    ADD CONSTRAINT chk_subscription_pricing_model_v3_compat
    CHECK (model IS NULL OR model IN ('tiered_graduated', 'all_usage', 'codec_multiplier', 'dimensioned')) NOT VALID;
ALTER TABLE purser.subscription_pricing_overrides
    DROP CONSTRAINT IF EXISTS chk_subscription_pricing_model;

ALTER TABLE purser.usage_records
    ADD COLUMN IF NOT EXISTS unit VARCHAR(32) NOT NULL DEFAULT 'unit',
    ADD COLUMN IF NOT EXISTS dimensions JSONB NOT NULL DEFAULT '{}',
    ADD COLUMN IF NOT EXISTS dimension_key CHAR(64) NOT NULL DEFAULT repeat('0', 64),
    ADD COLUMN IF NOT EXISTS source_id VARCHAR(128) NOT NULL DEFAULT 'legacy',
    ADD COLUMN IF NOT EXISTS report_id VARCHAR(64) NOT NULL DEFAULT repeat('0', 64);

CREATE UNIQUE INDEX IF NOT EXISTS usage_records_dimensioned_window_key
    ON purser.usage_records
    (tenant_id, cluster_id, source_id, usage_type, dimension_key, period_start, period_end);

DO $$
DECLARE
    constraint_name TEXT;
BEGIN
    SELECT conname INTO constraint_name
    FROM pg_constraint
    WHERE conrelid = 'purser.usage_records'::regclass
      AND contype = 'u'
      AND pg_get_constraintdef(oid) LIKE '%tenant_id, cluster_id, usage_type, period_start, period_end%';
    IF constraint_name IS NOT NULL THEN
        EXECUTE format('ALTER TABLE purser.usage_records DROP CONSTRAINT %I', constraint_name);
    END IF;
END $$;

ALTER TABLE purser.usage_adjustments
    ADD COLUMN IF NOT EXISTS unit VARCHAR(32) NOT NULL DEFAULT 'unit',
    ADD COLUMN IF NOT EXISTS dimensions JSONB NOT NULL DEFAULT '{}',
    ADD COLUMN IF NOT EXISTS dimension_key CHAR(64) NOT NULL DEFAULT repeat('0', 64);

ALTER TABLE purser.invoice_line_items
    ADD COLUMN IF NOT EXISTS unit VARCHAR(32) NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS dimensions JSONB NOT NULL DEFAULT '{}';

CREATE TABLE IF NOT EXISTS purser.provider_usage_records (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    usage_tenant_id UUID NOT NULL,
    work_cluster_id VARCHAR(100) NOT NULL,
    provider_tenant_id VARCHAR(100) NOT NULL DEFAULT '',
    provider_cluster_id VARCHAR(100) NOT NULL DEFAULT '',
    usage_type VARCHAR(64) NOT NULL,
    unit VARCHAR(32) NOT NULL,
    usage_value DECIMAL(20,6) NOT NULL,
    dimensions JSONB NOT NULL DEFAULT '{}',
    dimension_key CHAR(64) NOT NULL,
    source_id VARCHAR(128) NOT NULL,
    report_id VARCHAR(64) NOT NULL,
    period_start TIMESTAMPTZ NOT NULL,
    period_end TIMESTAMPTZ NOT NULL,
    granularity VARCHAR(20) NOT NULL DEFAULT 'minute_5',
    value_kind VARCHAR(20) NOT NULL DEFAULT 'delta',
    source VARCHAR(64) NOT NULL DEFAULT 'kafka',
    usage_details JSONB NOT NULL DEFAULT '{}',
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE (
        usage_tenant_id, work_cluster_id,
        provider_tenant_id, provider_cluster_id,
        source_id, usage_type, dimension_key,
        period_start, period_end
    )
);

CREATE TABLE IF NOT EXISTS purser.usage_reports (
    report_id VARCHAR(64) PRIMARY KEY,
    report_kind VARCHAR(20) NOT NULL,
    source_id VARCHAR(128) NOT NULL,
    source_region VARCHAR(64) NOT NULL DEFAULT '',
    sequence BIGINT NOT NULL,
    tenant_id UUID NOT NULL,
    cluster_id VARCHAR(100) NOT NULL,
    period_start TIMESTAMPTZ NOT NULL,
    period_end TIMESTAMPTZ NOT NULL,
    complete BOOLEAN NOT NULL,
    received_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
	CONSTRAINT chk_usage_reports_kind CHECK (report_kind IN ('finalized', 'reservation', 'window_complete')),
    UNIQUE (source_id, tenant_id, cluster_id, report_kind, sequence)
);

ALTER TABLE purser.usage_reports
    DROP CONSTRAINT IF EXISTS chk_usage_reports_kind;
ALTER TABLE purser.usage_reports
	ADD CONSTRAINT chk_usage_reports_kind
	CHECK (report_kind IN ('finalized', 'reservation', 'window_complete')) NOT VALID;

CREATE TABLE IF NOT EXISTS purser.usage_report_quarantine (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    report_id VARCHAR(64),
    source_id VARCHAR(128) NOT NULL DEFAULT '',
    tenant_id UUID,
    rejected_reason VARCHAR(100) NOT NULL,
    source_topic TEXT NOT NULL DEFAULT '',
    source_partition INTEGER,
    source_offset BIGINT,
    raw_payload JSONB NOT NULL DEFAULT '{}',
    rejected_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE TABLE IF NOT EXISTS purser.usage_reservations (
    tenant_id UUID NOT NULL,
    source_id VARCHAR(128) NOT NULL,
    cluster_id VARCHAR(100) NOT NULL,
    sequence BIGINT NOT NULL,
    report_id VARCHAR(64) NOT NULL,
    period_start TIMESTAMPTZ NOT NULL,
    period_end TIMESTAMPTZ NOT NULL,
    meters JSONB NOT NULL,
    reserved_amount_micro BIGINT NOT NULL DEFAULT 0,
    currency CHAR(3) NOT NULL,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (tenant_id, source_id, cluster_id)
);

CREATE INDEX IF NOT EXISTS idx_usage_reservations_tenant_currency_recent
    ON purser.usage_reservations (tenant_id, currency, updated_at DESC)
    INCLUDE (reserved_amount_micro);

CREATE TABLE IF NOT EXISTS purser.prepaid_usage_settlements (
    report_id VARCHAR(64) PRIMARY KEY,
    tenant_id UUID NOT NULL,
    billing_period_start TIMESTAMPTZ NOT NULL,
    billing_period_end TIMESTAMPTZ NOT NULL,
    amount_micro BIGINT NOT NULL,
    cumulative_amount_micro BIGINT NOT NULL,
    currency CHAR(3) NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_prepaid_usage_settlements_tenant_period
    ON purser.prepaid_usage_settlements (tenant_id, billing_period_start, billing_period_end);

CREATE TABLE IF NOT EXISTS purser.metering_sources (
    source_id VARCHAR(128) PRIMARY KEY,
    region VARCHAR(64) NOT NULL DEFAULT '',
    active_from TIMESTAMPTZ NOT NULL,
    active_until TIMESTAMPTZ,
    required BOOLEAN NOT NULL DEFAULT TRUE,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE TABLE IF NOT EXISTS purser.metering_windows (
    source_id VARCHAR(128) NOT NULL REFERENCES purser.metering_sources(source_id),
    period_start TIMESTAMPTZ NOT NULL,
    period_end TIMESTAMPTZ NOT NULL,
    complete BOOLEAN NOT NULL,
    report_count BIGINT NOT NULL DEFAULT 0,
    observed_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (source_id, period_start, period_end)
);

CREATE TABLE IF NOT EXISTS purser.metering_anomalies (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    source_id VARCHAR(128) NOT NULL,
    tenant_id UUID,
    cluster_id VARCHAR(100),
    anomaly_type VARCHAR(64) NOT NULL,
    source_event_id VARCHAR(255) NOT NULL,
    details JSONB NOT NULL DEFAULT '{}',
    status VARCHAR(20) NOT NULL DEFAULT 'open',
    resolution_reason TEXT,
    resolved_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE (source_id, anomaly_type, source_event_id),
    CONSTRAINT chk_metering_anomalies_status CHECK (status IN ('open', 'resolved', 'ignored'))
);

CREATE INDEX IF NOT EXISTS idx_provider_usage_provider_period
    ON purser.provider_usage_records(provider_tenant_id, period_start, period_end);
CREATE INDEX IF NOT EXISTS idx_provider_usage_tenant_period
    ON purser.provider_usage_records(usage_tenant_id, period_start, period_end);
CREATE INDEX IF NOT EXISTS idx_provider_usage_provider_cluster
    ON purser.provider_usage_records(provider_cluster_id, usage_type, period_start);
CREATE INDEX IF NOT EXISTS idx_usage_reports_source_period
    ON purser.usage_reports(source_id, period_start, period_end);
CREATE INDEX IF NOT EXISTS idx_usage_report_quarantine_source
    ON purser.usage_report_quarantine(source_id, rejected_at DESC);
CREATE INDEX IF NOT EXISTS idx_metering_anomalies_open
    ON purser.metering_anomalies(source_id, tenant_id, created_at)
    WHERE status = 'open';

ALTER TABLE purser.operator_credit_ledger
    ADD COLUMN IF NOT EXISTS provider_usage_record_id UUID;
DO $$
BEGIN
    IF EXISTS (
        SELECT 1
        FROM information_schema.columns
        WHERE table_schema = 'purser'
          AND table_name = 'operator_credit_ledger'
          AND column_name = 'storage_provider_usage_record_id'
    ) THEN
        ALTER TABLE purser.operator_credit_ledger
            DROP CONSTRAINT IF EXISTS chk_op_credit_source_v3_compat;
        ALTER TABLE purser.operator_credit_ledger
            ADD CONSTRAINT chk_op_credit_source_v3_compat CHECK (
                (source_type = 'invoice_line'           AND invoice_line_item_id IS NOT NULL) OR
                (source_type = 'storage_provider_usage' AND storage_provider_usage_record_id IS NOT NULL) OR
                (source_type = 'provider_usage'         AND provider_usage_record_id IS NOT NULL) OR
                (source_type = 'usage_adjustment'       AND usage_adjustment_id IS NOT NULL) OR
                (source_type = 'stripe_subscription'    AND stripe_invoice_id IS NOT NULL)
            ) NOT VALID;
        ALTER TABLE purser.operator_credit_ledger
            DROP CONSTRAINT IF EXISTS chk_op_credit_source;
    END IF;
END $$;
CREATE UNIQUE INDEX IF NOT EXISTS uq_op_credit_accrual_provider_usage
    ON purser.operator_credit_ledger(provider_usage_record_id)
    WHERE entry_type = 'accrual' AND source_type = 'provider_usage';

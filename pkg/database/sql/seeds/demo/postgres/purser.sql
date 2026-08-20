-- Deterministic local demo fixture owned by purser.
-- Applied explicitly by `make seed-demo`; never loaded by database first boot.

-- Billing tiers required by the demo subscription rows below. Production
-- clusters reconcile the canonical catalog through purser bootstrap; this file
-- is only the Docker dev/demo fixture loaded on a fresh volume.
--
-- Pricing rules (purser.tier_pricing_rules) and entitlements
-- (purser.tier_entitlements) are seeded below as separate rows.
WITH demo_process_config AS (
    SELECT
        '[{"process":"AV","codec":"opus","track_inhibit":"audio=opus","track_select":"audio=all&video=none&subtitle=none","x-LSP-name":"Audio to Opus"},{"process":"AV","codec":"AAC","track_inhibit":"audio=aac","track_select":"audio=all&video=none&subtitle=none","x-LSP-name":"Audio to AAC"},{"process":"Thumbs","track_select":"video=lowres","x-LSP-name":"Thumbnail Sprites"}]'::jsonb AS processes_live,
        '[{"process":"Thumbs","track_select":"video=maxbps","track_inhibit":"subtitle=all","inconsequential":true,"exit_unmask":true,"x-LSP-name":"Thumbnail Sprites"}]'::jsonb AS processes_dvr,
        '[{"process":"Thumbs","track_select":"video=maxbps","track_inhibit":"subtitle=all","inconsequential":true,"exit_unmask":true,"x-LSP-name":"Thumbnail Sprites"}]'::jsonb AS processes_clip,
        '[{"process":"Thumbs","track_select":"video=maxbps","track_inhibit":"subtitle=all","inconsequential":true,"exit_unmask":true,"x-LSP-name":"Thumbnail Sprites"}]'::jsonb AS processes_dvr_finalize,
        '[{"process":"AV","codec":"opus","track_inhibit":"audio=opus","track_select":"audio=all&video=none&subtitle=none"},{"process":"AV","codec":"AAC","track_inhibit":"audio=aac","track_select":"audio=all&video=none&subtitle=none"},{"process":"Livepeer","source_track":"maxbps","track_select":"video=maxbps","target_profiles":[{"name":"360p","bitrate":900000,"fps":0,"height":360,"profile":"H264ConstrainedHigh","track_inhibit":"video=<640x360"},{"name":"480p","bitrate":1600000,"fps":0,"height":480,"profile":"H264ConstrainedHigh","track_inhibit":"video=<850x480"},{"name":"720p","bitrate":3200000,"fps":0,"height":720,"profile":"H264ConstrainedHigh","track_inhibit":"video=<1280x720"},{"name":"1080p","bitrate":6500000,"fps":0,"height":1080,"profile":"H264ConstrainedHigh","track_inhibit":"video=<1920x1080"}],"track_inhibit":"video=<640x360"},{"process":"Thumbs","track_select":"video=maxbps","track_inhibit":"subtitle=all","inconsequential":true,"exit_unmask":true}]'::jsonb AS processes_vod
)
INSERT INTO purser.billing_tiers (
    tier_name, display_name, description, base_price, currency,
    features, support_level, sla_level, metering_enabled,
    tier_level, is_enterprise, is_default_prepaid, is_default_postpaid,
    processes_live, processes_dvr, processes_clip, processes_dvr_finalize, processes_vod
)
SELECT
    v.tier_name, v.display_name, v.description, v.base_price, v.currency,
    v.features::jsonb, v.support_level, v.sla_level, v.metering_enabled,
    v.tier_level, v.is_enterprise, v.is_default_prepaid, v.is_default_postpaid,
    pc.processes_live, pc.processes_dvr, pc.processes_clip, pc.processes_dvr_finalize, pc.processes_vod
FROM (VALUES
('payg', 'Pay As You Go', 'Prepaid pay-as-you-go pricing with no included usage.', 0.00, 'EUR',
'{"recording": true, "analytics": true, "api_access": true, "support_level": "community"}',
'community', 'none', true, 0, false, true, false),
('free', 'Free', 'Self-hosted with Livepeer transcoding. Watermarked player, no SLA.', 0.00, 'EUR',
'{"recording": true, "analytics": true, "api_access": true, "support_level": "community"}',
'community', 'none', true, 1, false, false, true),
('supporter', 'Supporter', '120K delivered mins, hosted LB, custom subdomain. ~100-300 viewers.', 79.00, 'EUR',
'{"recording": true, "analytics": true, "api_access": true, "support_level": "basic"}',
'basic', 'none', true, 2, false, false, false),
('developer', 'Developer', '500K delivered mins, priority processing, team features, advanced analytics. ~500-1K viewers.', 249.00, 'EUR',
'{"recording": true, "analytics": true, "api_access": true, "support_level": "priority"}',
'priority', 'standard', true, 3, false, false, false),
('production', 'Production', '2M delivered mins, dedicated processing capacity, 24/7 support + SLA. ~2-5K viewers.', 999.00, 'EUR',
'{"recording": true, "analytics": true, "api_access": true, "custom_branding": true, "sla": true, "support_level": "enterprise"}',
'enterprise', 'premium', true, 4, false, false, false),
('enterprise', 'Enterprise', 'Custom capacity, private deployments, dedicated support, custom SLAs. Contact us.', 0.00, 'EUR',
'{"recording": true, "analytics": true, "api_access": true, "custom_branding": true, "sla": true, "support_level": "dedicated", "processing_customizable": true}',
'dedicated', 'custom', true, 5, true, false, false)
) AS v(
    tier_name, display_name, description, base_price, currency,
    features, support_level, sla_level, metering_enabled,
    tier_level, is_enterprise, is_default_prepaid, is_default_postpaid
)
CROSS JOIN demo_process_config pc
ON CONFLICT (tier_name) DO UPDATE SET
    display_name = EXCLUDED.display_name,
    description = EXCLUDED.description,
    base_price = EXCLUDED.base_price,
    features = EXCLUDED.features,
    support_level = EXCLUDED.support_level,
    sla_level = EXCLUDED.sla_level,
    metering_enabled = EXCLUDED.metering_enabled,
    tier_level = EXCLUDED.tier_level,
    is_enterprise = EXCLUDED.is_enterprise,
    is_default_prepaid = EXCLUDED.is_default_prepaid,
    is_default_postpaid = EXCLUDED.is_default_postpaid,
    processes_live = EXCLUDED.processes_live,
    processes_dvr = EXCLUDED.processes_dvr,
    processes_clip = EXCLUDED.processes_clip,
    processes_dvr_finalize = EXCLUDED.processes_dvr_finalize,
    processes_vod = EXCLUDED.processes_vod;

-- Tier cap on customer-set retention. 0 = no cap (paid baseline);
-- Free's finite cap is the anti-abuse guardrail. Per-class system
-- defaults (VOD: keep forever, DVR/clip: 30d) live in Commodore code,
-- not in entitlements.
INSERT INTO purser.tier_entitlements (tier_id, key, value)
SELECT bt.id, 'recording_retention_days', to_jsonb(v.days)
FROM purser.billing_tiers bt
JOIN (VALUES
    ('free', 30), ('supporter', 0), ('developer', 0), ('production', 0)
) AS v(tier_name, days) ON v.tier_name = bt.tier_name
ON CONFLICT (tier_id, key) DO UPDATE SET value = EXCLUDED.value;

-- storage_limit_gb is the hard runtime cap on current durable artifact bytes
-- (point-in-time, distinct from the time-weighted storage_gb_seconds_cold
-- billing meter). Foghorn rejects new durable writes when a tenant is at cap.
-- Free tier only; paid tiers have no point-in-time cap (storage is metered).
INSERT INTO purser.tier_entitlements (tier_id, key, value)
SELECT bt.id, 'storage_limit_gb', to_jsonb(v.gb)
FROM purser.billing_tiers bt
JOIN (VALUES
    ('free', 10)
) AS v(tier_name, gb) ON v.tier_name = bt.tier_name
ON CONFLICT (tier_id, key) DO UPDATE SET value = EXCLUDED.value;

-- Free-plan concurrent fair-use caps. These are tenant-plan policy, not static
-- media-cluster capacity; cluster capacity is reported dynamically by edges.
INSERT INTO purser.tier_entitlements (tier_id, key, value)
SELECT bt.id, v.key, to_jsonb(v.value)
FROM purser.billing_tiers bt
JOIN (VALUES
    ('free', 'max_concurrent_streams', 3),
    ('free', 'max_concurrent_viewers', 200)
) AS v(tier_name, key, value) ON v.tier_name = bt.tier_name
ON CONFLICT (tier_id, key) DO UPDATE SET value = EXCLUDED.value;

-- Tier pricing rules (one row per tier x meter).
INSERT INTO purser.tier_pricing_rules (tier_id, meter, model, currency, included_quantity, unit_price, config)
SELECT bt.id, r.meter, r.model, 'EUR', r.included_quantity, r.unit_price, '{}'::jsonb
FROM purser.billing_tiers bt
JOIN (VALUES
    ('payg', 'delivered_minutes', 'tiered_graduated', 0, 0.00055),
    ('payg', 'storage_gb_seconds_cold', 'all_usage', 0, 0.035),
    ('free', 'delivered_minutes', 'tiered_graduated', 10000, 0),
    ('free', 'storage_gb_seconds_cold', 'tiered_graduated', 7200, 0),
    ('supporter', 'delivered_minutes', 'tiered_graduated', 120000, 0.00055),
    ('supporter', 'storage_gb_seconds_cold', 'all_usage', 0, 0.035),
    ('developer', 'delivered_minutes', 'tiered_graduated', 500000, 0.00052),
    ('developer', 'storage_gb_seconds_cold', 'all_usage', 0, 0.030),
    ('production', 'delivered_minutes', 'tiered_graduated', 2000000, 0.00050),
    ('production', 'storage_gb_seconds_cold', 'all_usage', 0, 0.025)
) AS r(tier_name, meter, model, included_quantity, unit_price)
ON r.tier_name = bt.tier_name
ON CONFLICT (tier_id, meter) DO UPDATE SET
    model = EXCLUDED.model,
    currency = EXCLUDED.currency,
    included_quantity = EXCLUDED.included_quantity,
    unit_price = EXCLUDED.unit_price,
    config = EXCLUDED.config;

-- Demo subscription in Purser
INSERT INTO purser.tenant_subscriptions (
    tenant_id, tier_id, status, billing_email, started_at, next_billing_date,
    billing_period_start, billing_period_end
)
SELECT
    '5eed517e-ba5e-da7a-517e-ba5eda7a0001', bt.id, 'active', 'demo@frameworks.network',
    NOW(), NOW() + INTERVAL '1 month',
    DATE_TRUNC('month', NOW()),
    DATE_TRUNC('month', NOW()) + INTERVAL '1 month'
FROM purser.billing_tiers bt
WHERE bt.tier_name = 'developer'
  AND NOT EXISTS (SELECT 1 FROM purser.tenant_subscriptions WHERE tenant_id = '5eed517e-ba5e-da7a-517e-ba5eda7a0001');

-- Demo Mollie customer + mandate for the demo tenant
INSERT INTO purser.mollie_customers (tenant_id, mollie_customer_id)
VALUES ('5eed517e-ba5e-da7a-517e-ba5eda7a0001', 'cst_demo_123')
ON CONFLICT (tenant_id) DO UPDATE SET
    mollie_customer_id = EXCLUDED.mollie_customer_id;

INSERT INTO purser.mollie_mandates (
    tenant_id, mollie_customer_id, mollie_mandate_id,
    status, method, details, created_at, updated_at
) VALUES (
    '5eed517e-ba5e-da7a-517e-ba5eda7a0001', 'cst_demo_123', 'mdt_demo_123',
    'valid', 'directdebit', '{"consumer_name":"Demo User","consumer_account":"NL00DEMO0000000000"}',
    NOW() - INTERVAL '30 days', NOW()
) ON CONFLICT (mollie_mandate_id) DO UPDATE SET
    status = EXCLUDED.status,
    method = EXCLUDED.method,
    details = EXCLUDED.details,
    updated_at = NOW();

-- Demo prepaid balance for the demo tenant (starts at EUR 50)
INSERT INTO purser.prepaid_balances (
    tenant_id, balance_cents, currency, low_balance_threshold_cents, created_at, updated_at
) VALUES (
    '5eed517e-ba5e-da7a-517e-ba5eda7a0001', 5000, 'EUR', 500, NOW() - INTERVAL '7 days', NOW()
) ON CONFLICT (tenant_id, currency) DO UPDATE SET
    balance_cents = EXCLUDED.balance_cents,
    updated_at = NOW();

-- Demo cluster subscription tracking (paid cluster flow uses this table)
INSERT INTO purser.cluster_subscriptions (
    tenant_id, cluster_id, status, created_at, updated_at
) VALUES
    ('5eed517e-ba5e-da7a-517e-ba5eda7a0001', 'central-primary', 'active', NOW(), NOW()),
    ('5eed517e-ba5e-da7a-517e-ba5eda7a0001', 'demo-media', 'active', NOW(), NOW()),
    ('5eed517e-ba5e-da7a-517e-ba5eda7a0001', 'demo-selfhosted', 'active', NOW(), NOW())
ON CONFLICT (tenant_id, cluster_id) DO UPDATE SET
    status = EXCLUDED.status,
    updated_at = NOW();

-- Cluster pricing for demo clusters
INSERT INTO purser.cluster_pricing (
    cluster_id, pricing_model,
    allow_free_tier, required_tier_level,
    default_quotas
) VALUES
    (
        'central-primary', 'free_unmetered',
        TRUE, 0,
        '{"retention_days": 7}'
    ),
    (
        'demo-selfhosted', 'free_unmetered',
        TRUE, 0,
        '{"retention_days": 30}'
    )
ON CONFLICT (cluster_id) DO UPDATE SET
    pricing_model = 'free_unmetered',
    allow_free_tier = TRUE;

INSERT INTO purser.platform_fee_policy (
    cluster_kind, cluster_owner_tenant_id, pricing_source, fee_basis_points, notes
)
VALUES (
    'third_party_marketplace', NULL, NULL, 2000, 'default marketplace revenue-share policy'
)
ON CONFLICT DO NOTHING;

-- ============================================================================
-- PURSER: 5-Minute Usage Records (raw data, like Periscope produces)
-- ============================================================================
-- Generate 7 days of 5-minute granularity usage records
-- 7 days * 24 hours * 12 intervals/hour = 2016 records per usage type
-- These are the canonical Purser rows used by invoices and usage charts.

-- 7 days of 5-minute delta rows on canonical, 5-min-aligned boundaries.
-- value_kind='delta' is required for rated meters to feed cluster_rating /
-- the rating engine; other value shapes are excluded from invoices.
INSERT INTO purser.usage_records (
    tenant_id, cluster_id, usage_type, unit, dimensions, dimension_key,
    source_id, report_id, usage_value, usage_details,
    period_start, period_end, granularity, value_kind
)
SELECT
    '5eed517e-ba5e-da7a-517e-ba5eda7a0001',
    'central-primary',
    usage_type,
    unit,
    dimensions,
    encode(digest(dimensions::text, 'sha256'), 'hex'),
    'demo-seed',
    encode(digest('demo-seed:' || usage_type || ':' || n::text, 'sha256'), 'hex'),
    CASE
        WHEN usage_type = 'storage_gb_seconds_cold' THEN base_value * 300.0 * (0.7 + 0.6 * random())
        WHEN usage_type IN ('max_viewers', 'total_streams', 'total_viewers') THEN base_value * (0.7 + 0.6 * random())
        ELSE base_value / 288.0 * (0.7 + 0.6 * random())
    END,
    '{}'::jsonb,
    date_trunc('hour', NOW()) + ((floor(extract(epoch FROM NOW() - date_trunc('hour', NOW())) / 300) - n) * INTERVAL '5 minutes'),
    date_trunc('hour', NOW()) + ((floor(extract(epoch FROM NOW() - date_trunc('hour', NOW())) / 300) - n + 1) * INTERVAL '5 minutes'),
    'minute_5',
    'delta'
FROM generate_series(0, 2015) AS n
CROSS JOIN (VALUES
    ('stream_runtime_seconds', 'second', '{}'::jsonb, 64800.0),
    ('ingress_gb', 'gibibyte', '{}'::jsonb, 18.0),
    ('egress_gb', 'gibibyte', '{}'::jsonb, 65.0),
    ('storage_gb_seconds_cold', 'gibibyte_second', '{"storage_backend":"object","storage_scope":"cold"}'::jsonb, 12.0),
    ('delivered_minutes', 'minute', '{}'::jsonb, 5100.0),
    ('transcode_rendition_seconds', 'second', '{"execution_backend":"mist","output_codec":"h264","track_type":"video"}'::jsonb, 3672.0),
    ('llm_input_tokens', 'token', '{"model":"demo","provider":"demo","service":"skipper"}'::jsonb, 12500.0),
    ('max_viewers', 'viewer', '{}'::jsonb, 140.0),
    ('total_streams', 'stream', '{}'::jsonb, 3.0),
    ('total_viewers', 'viewer', '{}'::jsonb, 420.0)
) AS usage_types(usage_type, unit, dimensions, base_value)
ON CONFLICT (tenant_id, cluster_id, source_id, usage_type, dimension_key, period_start, period_end) DO UPDATE SET
    usage_value = EXCLUDED.usage_value,
    granularity = EXCLUDED.granularity,
    value_kind = EXCLUDED.value_kind;

-- ============================================================================
-- PURSER: Demo Billing Invoices (Historical Invoice Records)
-- ============================================================================
-- Invoice history for billing page display
-- Note: status values are 'pending', 'paid', 'overdue', 'cancelled'

INSERT INTO purser.billing_invoices (
    id, tenant_id, status, currency, amount,
    period_start, period_end, due_date, paid_at,
    base_amount, metered_amount, gross_metered_amount, usage_details,
    created_at
) VALUES
-- Current month (draft invoice preview). gross_metered_amount == metered_amount
-- since usage is not waived in seed data.
(
    '5eedb111-fee5-da7a-b111-fee5da7a0001',
    '5eed517e-ba5e-da7a-517e-ba5eda7a0001',
    'draft',
    'EUR',
    249.71,
    DATE_TRUNC('month', NOW()),
    DATE_TRUNC('month', NOW() + INTERVAL '1 month'),
    DATE_TRUNC('month', NOW()) + INTERVAL '1 month' + INTERVAL '14 days',
    NULL,
    249.00,  -- Developer tier base
    0.71,    -- Storage: 23.5 GiB-hours x EUR 0.030
    0.71,    -- gross == metered (no waiver)
    '{"delivered_minutes": 250000.2, "storage_gb_seconds_cold": 84600.0, "stream_runtime_seconds": 459000.0, "ingress_gb": 82.0, "egress_gb": 456.78, "tier_info": {"tier_name": "developer", "display_name": "Developer", "base_price": 249.0, "metering_enabled": true}}',
    DATE_TRUNC('month', NOW())
),
-- Previous month (paid invoice)
(
    '5eedb111-fee5-da7a-b111-fee5da7a0002',
    '5eed517e-ba5e-da7a-517e-ba5eda7a0001',
    'paid',
    'EUR',
    250.36,
    DATE_TRUNC('month', NOW() - INTERVAL '1 month'),
    DATE_TRUNC('month', NOW()),
    DATE_TRUNC('month', NOW()) + INTERVAL '14 days',
    DATE_TRUNC('month', NOW()) + INTERVAL '5 days',
    249.00,
    1.36,
    1.36,    -- gross == metered (no waiver)
    '{"delivered_minutes": 450000.0, "storage_gb_seconds_cold": 162720.0, "stream_runtime_seconds": 1231200.0, "ingress_gb": 214.0, "egress_gb": 1245.6, "tier_info": {"tier_name": "developer", "display_name": "Developer", "base_price": 249.0, "metering_enabled": true}}',
    DATE_TRUNC('month', NOW() - INTERVAL '1 month')
),
-- Two months ago (paid invoice)
(
    '5eedb111-fee5-da7a-b111-fee5da7a0003',
    '5eed517e-ba5e-da7a-517e-ba5eda7a0001',
    'paid',
    'EUR',
    249.96,
    DATE_TRUNC('month', NOW() - INTERVAL '2 months'),
    DATE_TRUNC('month', NOW() - INTERVAL '1 month'),
    DATE_TRUNC('month', NOW()) - INTERVAL '1 month' + INTERVAL '14 days',
    DATE_TRUNC('month', NOW()) - INTERVAL '1 month' + INTERVAL '3 days',
    249.00,
    0.96,
    0.96,    -- gross == metered (no waiver)
    '{"delivered_minutes": 349999.8, "storage_gb_seconds_cold": 115560.0, "stream_runtime_seconds": 774900.0, "ingress_gb": 156.0, "egress_gb": 890.2, "tier_info": {"tier_name": "developer", "display_name": "Developer", "base_price": 249.0, "metering_enabled": true}}',
    DATE_TRUNC('month', NOW() - INTERVAL '2 months')
)
ON CONFLICT (id) DO UPDATE SET
    status = EXCLUDED.status,
    amount = EXCLUDED.amount,
    base_amount = EXCLUDED.base_amount,
    metered_amount = EXCLUDED.metered_amount,
    gross_metered_amount = EXCLUDED.gross_metered_amount,
    usage_details = EXCLUDED.usage_details,
    paid_at = EXCLUDED.paid_at,
    updated_at = NOW();

-- Demo invoice line items. The runtime writer stores these transactionally with
-- every invoice; seed data does the same so a fresh dev DB exercises the
-- line-item rendering path instead of falling back to invoice aggregates.
INSERT INTO purser.invoice_line_items (
    invoice_id, tenant_id, line_key, meter, unit, dimensions, description,
    quantity, included_quantity, billable_quantity,
    unit_price, amount, currency,
    cluster_id, cluster_kind, pricing_source
) VALUES
(
    '5eedb111-fee5-da7a-b111-fee5da7a0001',
    '5eed517e-ba5e-da7a-517e-ba5eda7a0001',
    'base_subscription',
    NULL,
    'subscription',
    '{}',
    'Base subscription',
    1, 0, 1, 249.00, 249.00, 'EUR',
    NULL, NULL, 'tier'
),
(
    '5eedb111-fee5-da7a-b111-fee5da7a0001',
    '5eed517e-ba5e-da7a-517e-ba5eda7a0001',
    'meter:delivered_minutes:demo-media:' || TO_CHAR(DATE_TRUNC('month', NOW()), 'YYYYMM'),
    'delivered_minutes',
    'minute',
    '{}',
    'Delivered minutes',
    250000, 500000, 0, 0.00052, 0.00, 'EUR',
    'demo-media', 'platform_official', 'tier'
),
(
    '5eedb111-fee5-da7a-b111-fee5da7a0001',
    '5eed517e-ba5e-da7a-517e-ba5eda7a0001',
    'meter:storage_gb_seconds_cold:demo-media:' || TO_CHAR(DATE_TRUNC('month', NOW()), 'YYYYMM'),
    'storage_gb_seconds_cold',
    'gibibyte_hour',
    '{"storage_backend":"object","storage_scope":"cold"}',
    'Cold storage',
    23.5, 0, 23.5, 0.030, 0.71, 'EUR',
    'demo-media', 'platform_official', 'tier'
),
(
    '5eedb111-fee5-da7a-b111-fee5da7a0002',
    '5eed517e-ba5e-da7a-517e-ba5eda7a0001',
    'base_subscription',
    NULL,
    'subscription',
    '{}',
    'Base subscription',
    1, 0, 1, 249.00, 249.00, 'EUR',
    NULL, NULL, 'tier'
),
(
    '5eedb111-fee5-da7a-b111-fee5da7a0002',
    '5eed517e-ba5e-da7a-517e-ba5eda7a0001',
    'meter:delivered_minutes:demo-media:' || TO_CHAR(DATE_TRUNC('month', NOW() - INTERVAL '1 month'), 'YYYYMM'),
    'delivered_minutes',
    'minute',
    '{}',
    'Delivered minutes',
    450000, 500000, 0, 0.00052, 0.00, 'EUR',
    'demo-media', 'platform_official', 'tier'
),
(
    '5eedb111-fee5-da7a-b111-fee5da7a0002',
    '5eed517e-ba5e-da7a-517e-ba5eda7a0001',
    'meter:storage_gb_seconds_cold:demo-media:' || TO_CHAR(DATE_TRUNC('month', NOW() - INTERVAL '1 month'), 'YYYYMM'),
    'storage_gb_seconds_cold',
    'gibibyte_hour',
    '{"storage_backend":"object","storage_scope":"cold"}',
    'Cold storage',
    45.2, 0, 45.2, 0.030, 1.36, 'EUR',
    'demo-media', 'platform_official', 'tier'
),
(
    '5eedb111-fee5-da7a-b111-fee5da7a0003',
    '5eed517e-ba5e-da7a-517e-ba5eda7a0001',
    'base_subscription',
    NULL,
    'subscription',
    '{}',
    'Base subscription',
    1, 0, 1, 249.00, 249.00, 'EUR',
    NULL, NULL, 'tier'
),
(
    '5eedb111-fee5-da7a-b111-fee5da7a0003',
    '5eed517e-ba5e-da7a-517e-ba5eda7a0001',
    'meter:delivered_minutes:demo-media:' || TO_CHAR(DATE_TRUNC('month', NOW() - INTERVAL '2 months'), 'YYYYMM'),
    'delivered_minutes',
    'minute',
    '{}',
    'Delivered minutes',
    350000, 500000, 0, 0.00052, 0.00, 'EUR',
    'demo-media', 'platform_official', 'tier'
),
(
    '5eedb111-fee5-da7a-b111-fee5da7a0003',
    '5eed517e-ba5e-da7a-517e-ba5eda7a0001',
    'meter:storage_gb_seconds_cold:demo-media:' || TO_CHAR(DATE_TRUNC('month', NOW() - INTERVAL '2 months'), 'YYYYMM'),
    'storage_gb_seconds_cold',
    'gibibyte_hour',
    '{"storage_backend":"object","storage_scope":"cold"}',
    'Cold storage',
    32.1, 0, 32.1, 0.030, 0.96, 'EUR',
    'demo-media', 'platform_official', 'tier'
)
ON CONFLICT (invoice_id, line_key) DO UPDATE SET
    tenant_id = EXCLUDED.tenant_id,
    meter = EXCLUDED.meter,
    unit = EXCLUDED.unit,
    dimensions = EXCLUDED.dimensions,
    description = EXCLUDED.description,
    quantity = EXCLUDED.quantity,
    included_quantity = EXCLUDED.included_quantity,
    billable_quantity = EXCLUDED.billable_quantity,
    unit_price = EXCLUDED.unit_price,
    amount = EXCLUDED.amount,
    currency = EXCLUDED.currency,
    cluster_id = EXCLUDED.cluster_id,
    cluster_kind = EXCLUDED.cluster_kind,
    pricing_source = EXCLUDED.pricing_source,
    updated_at = NOW();

-- ============================================================================
-- PURSER: Demo Billing Payments (Payment Transactions)
-- ============================================================================
-- Payment records linked to paid invoices

INSERT INTO purser.billing_payments (
    id, invoice_id, method, amount, currency, tx_id, status, confirmed_at, created_at
) VALUES
-- Payment for previous month invoice
(
    '5eedba1d-fee5-da7a-ba1d-fee5da7a0001',
    '5eedb111-fee5-da7a-b111-fee5da7a0002',  -- Previous month paid invoice
    'card',
    250.36,
    'EUR',
    'tr_demo_sepa_001',
    'confirmed',
    DATE_TRUNC('month', NOW()) + INTERVAL '5 days',
    DATE_TRUNC('month', NOW()) + INTERVAL '5 days'
),
-- Payment for two months ago invoice
(
    '5eedba1d-fee5-da7a-ba1d-fee5da7a0002',
    '5eedb111-fee5-da7a-b111-fee5da7a0003',  -- Two months ago paid invoice
    'card',
    249.96,
    'EUR',
    'tr_demo_sepa_002',
    'confirmed',
    DATE_TRUNC('month', NOW()) - INTERVAL '1 month' + INTERVAL '3 days',
    DATE_TRUNC('month', NOW()) - INTERVAL '1 month' + INTERVAL '3 days'
)
ON CONFLICT (id) DO UPDATE SET
    amount = EXCLUDED.amount,
    currency = EXCLUDED.currency,
    status = EXCLUDED.status,
    confirmed_at = EXCLUDED.confirmed_at;

-- ============================================================================
-- PURSER: Demo Balance Transactions (Prepaid Audit Trail)
-- ============================================================================
-- Transaction history explaining the EUR 50 prepaid balance

INSERT INTO purser.balance_transactions (
    id, tenant_id, amount_cents, balance_after_cents, transaction_type, description,
    reference_id, reference_type, created_at
) VALUES
(
    '5eedba1a-5ce5-da7a-ba1a-5ce5da7a0001',
    '5eed517e-ba5e-da7a-517e-ba5eda7a0001',
    5000,       -- +EUR 50.00 topup
    5000,       -- Balance after: EUR 50.00
    'topup',
    'Initial demo balance - card payment',
    '5eedba1a-5ce5-da7a-ba1a-5ce5da7a0002',  -- Reference to a notional card payment
    'card_payment',
    NOW() - INTERVAL '7 days'
)
ON CONFLICT (tenant_id, reference_type, reference_id)
WHERE reference_type IS NOT NULL AND reference_id IS NOT NULL
DO UPDATE SET
    balance_after_cents = EXCLUDED.balance_after_cents;

-- ============================================================================
-- PURSER: Demo Tenant Balance Rollups (Statistics)
-- ============================================================================
-- Pre-aggregated lifetime stats matching balance_transactions

INSERT INTO purser.tenant_balance_rollups (
    tenant_id, total_topup_cents, total_topup_eur_cents, total_usage_cents,
    topup_count, first_topup_at, last_topup_at, created_at, updated_at
) VALUES (
    '5eed517e-ba5e-da7a-517e-ba5eda7a0001',
    5000,       -- EUR 50.00 total topups
    5000,       -- EUR 50.00 EUR equivalent
    0,          -- No usage deductions yet
    1,          -- 1 topup
    NOW() - INTERVAL '7 days',
    NOW() - INTERVAL '7 days',
    NOW() - INTERVAL '7 days',
    NOW()
)
ON CONFLICT (tenant_id) DO UPDATE SET
    total_topup_cents = EXCLUDED.total_topup_cents,
    total_topup_eur_cents = EXCLUDED.total_topup_eur_cents,
    total_usage_cents = EXCLUDED.total_usage_cents,
    topup_count = EXCLUDED.topup_count,
    updated_at = NOW();

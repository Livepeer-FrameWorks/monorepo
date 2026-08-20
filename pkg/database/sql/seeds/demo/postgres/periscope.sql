-- Deterministic local demo fixture owned by periscope.
-- Applied explicitly by `make seed-demo`; never loaded by database first boot.

-- ============================================================================
-- PERISCOPE: Billing Cursors (Analytics Aggregation Checkpoints)
-- ============================================================================
-- Tracks the demo source activation boundary and last acknowledged metering
-- window. The source id matches docker-compose's default metering worker.

INSERT INTO periscope.metering_sources (
    source_id, source_region, activated_at
) VALUES (
    'periscope-local',
    'local',
    NOW() - INTERVAL '1 hour'
) ON CONFLICT (source_id) DO UPDATE SET
    source_region = EXCLUDED.source_region;

INSERT INTO periscope.billing_cursors (
    source_id, tenant_id, last_processed_at, updated_at
) VALUES (
    'periscope-local',
    '5eed517e-ba5e-da7a-517e-ba5eda7a0001',
    NOW() - INTERVAL '1 hour',  -- Last processed 1 hour ago
    NOW()
) ON CONFLICT (source_id, tenant_id) DO UPDATE SET
    last_processed_at = NOW() - INTERVAL '1 hour',
    updated_at = NOW();

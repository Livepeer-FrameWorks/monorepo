-- ============================================================================
-- BILLING TIER SEED DATA (PRODUCTION READY)
-- ============================================================================
-- Values sourced from website_marketing/src/components/pages/Pricing.jsx
-- ============================================================================
--
-- STRUCTURE REFERENCE (matches Go models):
--
--   AllocationDetails: {"limit": <float|null>, "unit": "<string>", "unit_price": <float>}
--     - limit: null = unlimited, otherwise included amount
--     - unit: measurement unit (delivered_minutes, retention_days, gpu_hours)
--     - unit_price: overage cost per unit (0 if no overage billing)
--
--   OverageRates: {"bandwidth": {...}, "storage": {...}, "compute": {...}}
--
--   BillingFeatures: {"recording": <bool>, "analytics": <bool>, "api_access": <bool>,
--                     "custom_branding": <bool>, "sla": <bool>, "support_level": "<string>"}
--
-- NOTE: Enforcement limits (max_streams, max_viewers, bandwidth caps) belong in
--       quartermaster.tenant_cluster_assignments, NOT here. This is BILLING only.
-- ============================================================================

INSERT INTO purser.billing_tiers (tier_name, display_name, description, base_price, currency, bandwidth_allocation, storage_allocation, compute_allocation, features, support_level, sla_level, metering_enabled, overage_rates, sort_order, is_enterprise) VALUES

-- Free Tier (self-hosted, no overages)
('free', 'Free', 'Self-hosted with Livepeer transcoding. Watermarked player, no SLA.', 0.00, 'EUR',
'{"limit": null, "unit": "delivered_minutes", "unit_price": 0}',
'{"limit": 30, "unit": "retention_days", "unit_price": 0}',
'{"limit": 0, "unit": "gpu_hours", "unit_price": 0}',
'{"recording": false, "analytics": true, "api_access": true, "support_level": "community"}',
'community', 'none', false,
'{"bandwidth": {"limit": null, "unit": "delivered_minutes", "unit_price": 0}, "storage": {"limit": null, "unit": "gb", "unit_price": 0}, "compute": {"limit": null, "unit": "gpu_hours", "unit_price": 0}}',
1, false),

-- Supporter Tier (€79/mo)
('supporter', 'Supporter', '150K delivered mins, 10 GPU-hrs, hosted LB, custom subdomain. ~100-300 viewers.', 79.00, 'EUR',
'{"limit": 150000, "unit": "delivered_minutes", "unit_price": 0.00049}',
'{"limit": 90, "unit": "retention_days", "unit_price": 0}',
'{"limit": 10, "unit": "gpu_hours", "unit_price": 0.50}',
'{"recording": true, "analytics": true, "api_access": true, "support_level": "basic"}',
'basic', 'none', true,
'{"bandwidth": {"limit": null, "unit": "delivered_minutes", "unit_price": 0.00049}, "storage": {"limit": null, "unit": "gb", "unit_price": 0.01}, "compute": {"limit": null, "unit": "gpu_hours", "unit_price": 0.50}}',
2, false),

-- Developer Tier (€249/mo)
('developer', 'Developer', '500K delivered mins, 50 GPU-hrs (priority), team features, advanced analytics. ~500-1K viewers.', 249.00, 'EUR',
'{"limit": 500000, "unit": "delivered_minutes", "unit_price": 0.00047}',
'{"limit": 180, "unit": "retention_days", "unit_price": 0}',
'{"limit": 50, "unit": "gpu_hours", "unit_price": 0.50}',
'{"recording": true, "analytics": true, "api_access": true, "support_level": "priority"}',
'priority', 'standard', true,
'{"bandwidth": {"limit": null, "unit": "delivered_minutes", "unit_price": 0.00047}, "storage": {"limit": null, "unit": "gb", "unit_price": 0.008}, "compute": {"limit": null, "unit": "gpu_hours", "unit_price": 0.50}}',
3, false),

-- Production Tier (€999/mo)
('production', 'Production', '2M delivered mins, 250 GPU-hrs, dedicated capacity, 24/7 support + SLA. ~2-5K viewers.', 999.00, 'EUR',
'{"limit": 2000000, "unit": "delivered_minutes", "unit_price": 0.00045}',
'{"limit": 365, "unit": "retention_days", "unit_price": 0}',
'{"limit": 250, "unit": "gpu_hours", "unit_price": 0.50}',
'{"recording": true, "analytics": true, "api_access": true, "custom_branding": true, "sla": true, "support_level": "enterprise"}',
'enterprise', 'premium', true,
'{"bandwidth": {"limit": null, "unit": "delivered_minutes", "unit_price": 0.00045}, "storage": {"limit": null, "unit": "gb", "unit_price": 0.005}, "compute": {"limit": null, "unit": "gpu_hours", "unit_price": 0.50}}',
4, false),

-- Enterprise Tier (custom pricing)
('enterprise', 'Enterprise', 'Custom capacity, private deployments, dedicated support, custom SLAs. Contact us.', 0.00, 'EUR',
'{"limit": null, "unit": "delivered_minutes", "unit_price": 0}',
'{"limit": null, "unit": "retention_days", "unit_price": 0}',
'{"limit": null, "unit": "gpu_hours", "unit_price": 0}',
'{"recording": true, "analytics": true, "api_access": true, "custom_branding": true, "sla": true, "support_level": "dedicated"}',
'dedicated', 'custom', true,
'{"bandwidth": {"limit": null, "unit": "delivered_minutes", "unit_price": 0}, "storage": {"limit": null, "unit": "gb", "unit_price": 0}, "compute": {"limit": null, "unit": "gpu_hours", "unit_price": 0}}',
5, true)

ON CONFLICT (tier_name) DO UPDATE SET
    display_name = EXCLUDED.display_name,
    description = EXCLUDED.description,
    base_price = EXCLUDED.base_price,
    bandwidth_allocation = EXCLUDED.bandwidth_allocation,
    storage_allocation = EXCLUDED.storage_allocation,
    compute_allocation = EXCLUDED.compute_allocation,
    features = EXCLUDED.features,
    support_level = EXCLUDED.support_level,
    sla_level = EXCLUDED.sla_level,
    metering_enabled = EXCLUDED.metering_enabled,
    overage_rates = EXCLUDED.overage_rates,
    sort_order = EXCLUDED.sort_order,
    is_enterprise = EXCLUDED.is_enterprise;

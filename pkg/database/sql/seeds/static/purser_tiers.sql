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
--   OverageRates: {"bandwidth": {...}, "storage": {...}, "compute": {...}, "processing": {...}}
--
--   ProcessingRates: {"h264_rate_per_min": <float>, "codec_multipliers": {"h264": 1.0, "hevc": 1.5, "vp9": 1.5, "av1": 2.0}}
--     - h264_rate_per_min: base rate per minute (0 = free transcoding)
--     - codec_multipliers: relative cost multiplier by codec (hevc/vp9 = 1.5x, av1 = 2x)
--
--   BillingFeatures: {"recording": <bool>, "analytics": <bool>, "api_access": <bool>,
--                     "custom_branding": <bool>, "sla": <bool>, "support_level": "<string>",
--                     "processing_customizable": <bool>}
--     - processing_customizable: tenant can override tier process config (enterprise only)
--
--   processes_live / processes_vod: Raw MistServer process JSON arrays.
--     - Use {{gateway_url}} placeholder for Livepeer broadcaster address.
--     - Foghorn substitutes at cache/dispatch time from its local cluster's gateway.
--
-- NOTE: Enforcement limits (max_streams, max_viewers, bandwidth caps) belong in
--       quartermaster.tenant_cluster_assignments, NOT here. This is BILLING only.
-- ============================================================================

INSERT INTO purser.billing_tiers (tier_name, display_name, description, base_price, currency, bandwidth_allocation, storage_allocation, compute_allocation, features, support_level, sla_level, metering_enabled, overage_rates, tier_level, is_enterprise, is_default_prepaid, is_default_postpaid, processes_live, processes_vod) VALUES

-- Pay-As-You-Go Tier (prepaid, no included allocations)
('payg', 'Pay As You Go', 'Prepaid pay-as-you-go pricing with no included allocations.', 0.00, 'EUR',
'{"limit": 0, "unit": "delivered_minutes", "unit_price": 0.00049}',
'{"limit": 0, "unit": "gb", "unit_price": 0.01}',
'{"limit": 0, "unit": "gpu_hours", "unit_price": 0.50}',
'{"recording": true, "analytics": true, "api_access": true, "support_level": "community"}',
'community', 'none', true,
'{"bandwidth": {"limit": null, "unit": "delivered_minutes", "unit_price": 0.00049}, "storage": {"limit": null, "unit": "gb", "unit_price": 0.01}, "compute": {"limit": null, "unit": "gpu_hours", "unit_price": 0.50}, "processing": {"h264_rate_per_min": 0, "codec_multipliers": {"h264": 1.0, "hevc": 1.5, "vp9": 1.5, "av1": 2.0}}}',
0, false, true, false,
'[{"process":"AV","codec":"opus","track_inhibit":"audio=opus","track_select":"video=none","x-LSP-name":"Audio to Opus"},{"process":"AV","codec":"AAC","track_inhibit":"audio=aac","track_select":"video=none","x-LSP-name":"Audio to AAC"},{"process":"Livepeer","hardcoded_broadcasters":"[{\"address\":\"{{gateway_url}}\"}]","target_profiles":[{"name":"480p","bitrate":512000,"fps":15,"height":480,"profile":"H264ConstrainedHigh","track_inhibit":"video=<850x480"},{"name":"720p","bitrate":1024000,"fps":25,"height":720,"profile":"H264ConstrainedHigh","track_inhibit":"video=<1281x720"}],"track_inhibit":"video=<850x480","x-LSP-name":"ABR Transcode"},{"process":"Thumbs","x-LSP-name":"Thumbnail Sprites"}]',
'[{"process":"AV","codec":"opus","track_inhibit":"audio=opus","track_select":"video=none"},{"process":"AV","codec":"AAC","track_inhibit":"audio=aac","track_select":"video=none"},{"process":"Livepeer","hardcoded_broadcasters":"[{\"address\":\"{{gateway_url}}\"}]","target_profiles":[{"name":"480p","bitrate":512000,"fps":15,"height":480,"profile":"VP9Profile0","track_inhibit":"video=<850x480"},{"name":"720p","bitrate":1024000,"fps":25,"height":720,"profile":"VP9Profile0","track_inhibit":"video=<1281x720"}],"track_inhibit":"video=<850x480"},{"process":"Thumbs","track_select":"video=maxbps","track_inhibit":"subtitle=all","inconsequential":true,"exit_unmask":true}]'),

-- Free Tier (self-hosted, no overages)
('free', 'Free', 'Self-hosted with Livepeer transcoding. Watermarked player, no SLA.', 0.00, 'EUR',
'{"limit": null, "unit": "delivered_minutes", "unit_price": 0}',
'{"limit": 30, "unit": "retention_days", "unit_price": 0}',
'{"limit": 0, "unit": "gpu_hours", "unit_price": 0}',
'{"recording": false, "analytics": true, "api_access": true, "support_level": "community"}',
'community', 'none', false,
'{"bandwidth": {"limit": null, "unit": "delivered_minutes", "unit_price": 0}, "storage": {"limit": null, "unit": "gb", "unit_price": 0}, "compute": {"limit": null, "unit": "gpu_hours", "unit_price": 0}, "processing": {"h264_rate_per_min": 0, "codec_multipliers": {"h264": 1.0, "hevc": 1.5, "vp9": 1.5, "av1": 2.0}}}',
1, false, false, true,
'[{"process":"AV","codec":"opus","track_inhibit":"audio=opus","track_select":"video=none","x-LSP-name":"Audio to Opus"},{"process":"AV","codec":"AAC","track_inhibit":"audio=aac","track_select":"video=none","x-LSP-name":"Audio to AAC"},{"process":"Livepeer","hardcoded_broadcasters":"[{\"address\":\"{{gateway_url}}\"}]","target_profiles":[{"name":"480p","bitrate":512000,"fps":15,"height":480,"profile":"H264ConstrainedHigh","track_inhibit":"video=<850x480"},{"name":"720p","bitrate":1024000,"fps":25,"height":720,"profile":"H264ConstrainedHigh","track_inhibit":"video=<1281x720"}],"track_inhibit":"video=<850x480","x-LSP-name":"ABR Transcode"},{"process":"Thumbs","x-LSP-name":"Thumbnail Sprites"}]',
'[{"process":"AV","codec":"opus","track_inhibit":"audio=opus","track_select":"video=none"},{"process":"AV","codec":"AAC","track_inhibit":"audio=aac","track_select":"video=none"},{"process":"Livepeer","hardcoded_broadcasters":"[{\"address\":\"{{gateway_url}}\"}]","target_profiles":[{"name":"480p","bitrate":512000,"fps":15,"height":480,"profile":"VP9Profile0","track_inhibit":"video=<850x480"},{"name":"720p","bitrate":1024000,"fps":25,"height":720,"profile":"VP9Profile0","track_inhibit":"video=<1281x720"}],"track_inhibit":"video=<850x480"},{"process":"Thumbs","track_select":"video=maxbps","track_inhibit":"subtitle=all","inconsequential":true,"exit_unmask":true}]'),

-- Supporter Tier (79/mo)
('supporter', 'Supporter', '150K delivered mins, 10 GPU-hrs, hosted LB, custom subdomain. ~100-300 viewers.', 79.00, 'EUR',
'{"limit": 150000, "unit": "delivered_minutes", "unit_price": 0.00049}',
'{"limit": 90, "unit": "retention_days", "unit_price": 0}',
'{"limit": 10, "unit": "gpu_hours", "unit_price": 0.50}',
'{"recording": true, "analytics": true, "api_access": true, "support_level": "basic"}',
'basic', 'none', true,
'{"bandwidth": {"limit": null, "unit": "delivered_minutes", "unit_price": 0.00049}, "storage": {"limit": null, "unit": "gb", "unit_price": 0.01}, "compute": {"limit": null, "unit": "gpu_hours", "unit_price": 0.50}, "processing": {"h264_rate_per_min": 0, "codec_multipliers": {"h264": 1.0, "hevc": 1.5, "vp9": 1.5, "av1": 2.0}}}',
2, false, false, false,
'[{"process":"AV","codec":"opus","track_inhibit":"audio=opus","track_select":"video=none","x-LSP-name":"Audio to Opus"},{"process":"AV","codec":"AAC","track_inhibit":"audio=aac","track_select":"video=none","x-LSP-name":"Audio to AAC"},{"process":"Livepeer","hardcoded_broadcasters":"[{\"address\":\"{{gateway_url}}\"}]","target_profiles":[{"name":"480p","bitrate":512000,"fps":15,"height":480,"profile":"H264ConstrainedHigh","track_inhibit":"video=<850x480"},{"name":"720p","bitrate":1024000,"fps":25,"height":720,"profile":"H264ConstrainedHigh","track_inhibit":"video=<1281x720"}],"track_inhibit":"video=<850x480","x-LSP-name":"ABR Transcode"},{"process":"Thumbs","x-LSP-name":"Thumbnail Sprites"}]',
'[{"process":"AV","codec":"opus","track_inhibit":"audio=opus","track_select":"video=none"},{"process":"AV","codec":"AAC","track_inhibit":"audio=aac","track_select":"video=none"},{"process":"Livepeer","hardcoded_broadcasters":"[{\"address\":\"{{gateway_url}}\"}]","target_profiles":[{"name":"480p","bitrate":512000,"fps":15,"height":480,"profile":"VP9Profile0","track_inhibit":"video=<850x480"},{"name":"720p","bitrate":1024000,"fps":25,"height":720,"profile":"VP9Profile0","track_inhibit":"video=<1281x720"}],"track_inhibit":"video=<850x480"},{"process":"Thumbs","track_select":"video=maxbps","track_inhibit":"subtitle=all","inconsequential":true,"exit_unmask":true}]'),

-- Developer Tier (249/mo)
('developer', 'Developer', '500K delivered mins, 50 GPU-hrs (priority), team features, advanced analytics. ~500-1K viewers.', 249.00, 'EUR',
'{"limit": 500000, "unit": "delivered_minutes", "unit_price": 0.00047}',
'{"limit": 180, "unit": "retention_days", "unit_price": 0}',
'{"limit": 50, "unit": "gpu_hours", "unit_price": 0.50}',
'{"recording": true, "analytics": true, "api_access": true, "support_level": "priority"}',
'priority', 'standard', true,
'{"bandwidth": {"limit": null, "unit": "delivered_minutes", "unit_price": 0.00047}, "storage": {"limit": null, "unit": "gb", "unit_price": 0.008}, "compute": {"limit": null, "unit": "gpu_hours", "unit_price": 0.50}, "processing": {"h264_rate_per_min": 0, "codec_multipliers": {"h264": 1.0, "hevc": 1.5, "vp9": 1.5, "av1": 2.0}}}',
3, false, false, false,
'[{"process":"AV","codec":"opus","track_inhibit":"audio=opus","track_select":"video=none","x-LSP-name":"Audio to Opus"},{"process":"AV","codec":"AAC","track_inhibit":"audio=aac","track_select":"video=none","x-LSP-name":"Audio to AAC"},{"process":"Livepeer","hardcoded_broadcasters":"[{\"address\":\"{{gateway_url}}\"}]","target_profiles":[{"name":"480p","bitrate":512000,"fps":15,"height":480,"profile":"H264ConstrainedHigh","track_inhibit":"video=<850x480"},{"name":"720p","bitrate":1024000,"fps":25,"height":720,"profile":"H264ConstrainedHigh","track_inhibit":"video=<1281x720"}],"track_inhibit":"video=<850x480","x-LSP-name":"ABR Transcode"},{"process":"Thumbs","x-LSP-name":"Thumbnail Sprites"}]',
'[{"process":"AV","codec":"opus","track_inhibit":"audio=opus","track_select":"video=none"},{"process":"AV","codec":"AAC","track_inhibit":"audio=aac","track_select":"video=none"},{"process":"Livepeer","hardcoded_broadcasters":"[{\"address\":\"{{gateway_url}}\"}]","target_profiles":[{"name":"480p","bitrate":512000,"fps":15,"height":480,"profile":"VP9Profile0","track_inhibit":"video=<850x480"},{"name":"720p","bitrate":1024000,"fps":25,"height":720,"profile":"VP9Profile0","track_inhibit":"video=<1281x720"}],"track_inhibit":"video=<850x480"},{"process":"Thumbs","track_select":"video=maxbps","track_inhibit":"subtitle=all","inconsequential":true,"exit_unmask":true}]'),

-- Production Tier (999/mo)
('production', 'Production', '2M delivered mins, 250 GPU-hrs, dedicated capacity, 24/7 support + SLA. ~2-5K viewers.', 999.00, 'EUR',
'{"limit": 2000000, "unit": "delivered_minutes", "unit_price": 0.00045}',
'{"limit": 365, "unit": "retention_days", "unit_price": 0}',
'{"limit": 250, "unit": "gpu_hours", "unit_price": 0.50}',
'{"recording": true, "analytics": true, "api_access": true, "custom_branding": true, "sla": true, "support_level": "enterprise"}',
'enterprise', 'premium', true,
'{"bandwidth": {"limit": null, "unit": "delivered_minutes", "unit_price": 0.00045}, "storage": {"limit": null, "unit": "gb", "unit_price": 0.005}, "compute": {"limit": null, "unit": "gpu_hours", "unit_price": 0.50}, "processing": {"h264_rate_per_min": 0, "codec_multipliers": {"h264": 1.0, "hevc": 1.5, "vp9": 1.5, "av1": 2.0}}}',
4, false, false, false,
'[{"process":"AV","codec":"opus","track_inhibit":"audio=opus","track_select":"video=none","x-LSP-name":"Audio to Opus"},{"process":"AV","codec":"AAC","track_inhibit":"audio=aac","track_select":"video=none","x-LSP-name":"Audio to AAC"},{"process":"Livepeer","hardcoded_broadcasters":"[{\"address\":\"{{gateway_url}}\"}]","target_profiles":[{"name":"480p","bitrate":512000,"fps":15,"height":480,"profile":"H264ConstrainedHigh","track_inhibit":"video=<850x480"},{"name":"720p","bitrate":1024000,"fps":25,"height":720,"profile":"H264ConstrainedHigh","track_inhibit":"video=<1281x720"}],"track_inhibit":"video=<850x480","x-LSP-name":"ABR Transcode"},{"process":"Thumbs","x-LSP-name":"Thumbnail Sprites"}]',
'[{"process":"AV","codec":"opus","track_inhibit":"audio=opus","track_select":"video=none"},{"process":"AV","codec":"AAC","track_inhibit":"audio=aac","track_select":"video=none"},{"process":"Livepeer","hardcoded_broadcasters":"[{\"address\":\"{{gateway_url}}\"}]","target_profiles":[{"name":"480p","bitrate":512000,"fps":15,"height":480,"profile":"VP9Profile0","track_inhibit":"video=<850x480"},{"name":"720p","bitrate":1024000,"fps":25,"height":720,"profile":"VP9Profile0","track_inhibit":"video=<1281x720"}],"track_inhibit":"video=<850x480"},{"process":"Thumbs","track_select":"video=maxbps","track_inhibit":"subtitle=all","inconsequential":true,"exit_unmask":true}]'),

-- Enterprise Tier (custom pricing)
('enterprise', 'Enterprise', 'Custom capacity, private deployments, dedicated support, custom SLAs. Contact us.', 0.00, 'EUR',
'{"limit": null, "unit": "delivered_minutes", "unit_price": 0}',
'{"limit": null, "unit": "retention_days", "unit_price": 0}',
'{"limit": null, "unit": "gpu_hours", "unit_price": 0}',
'{"recording": true, "analytics": true, "api_access": true, "custom_branding": true, "sla": true, "support_level": "dedicated", "processing_customizable": true}',
'dedicated', 'custom', true,
'{"bandwidth": {"limit": null, "unit": "delivered_minutes", "unit_price": 0}, "storage": {"limit": null, "unit": "gb", "unit_price": 0}, "compute": {"limit": null, "unit": "gpu_hours", "unit_price": 0}, "processing": {"h264_rate_per_min": 0, "codec_multipliers": {"h264": 1.0, "hevc": 1.5, "vp9": 1.5, "av1": 2.0}}}',
5, true, false, false,
'[{"process":"AV","codec":"opus","track_inhibit":"audio=opus","track_select":"video=none","x-LSP-name":"Audio to Opus"},{"process":"AV","codec":"AAC","track_inhibit":"audio=aac","track_select":"video=none","x-LSP-name":"Audio to AAC"},{"process":"Livepeer","hardcoded_broadcasters":"[{\"address\":\"{{gateway_url}}\"}]","target_profiles":[{"name":"480p","bitrate":512000,"fps":15,"height":480,"profile":"H264ConstrainedHigh","track_inhibit":"video=<850x480"},{"name":"720p","bitrate":1024000,"fps":25,"height":720,"profile":"H264ConstrainedHigh","track_inhibit":"video=<1281x720"}],"track_inhibit":"video=<850x480","x-LSP-name":"ABR Transcode"},{"process":"Thumbs","x-LSP-name":"Thumbnail Sprites"}]',
'[{"process":"AV","codec":"opus","track_inhibit":"audio=opus","track_select":"video=none"},{"process":"AV","codec":"AAC","track_inhibit":"audio=aac","track_select":"video=none"},{"process":"Livepeer","hardcoded_broadcasters":"[{\"address\":\"{{gateway_url}}\"}]","target_profiles":[{"name":"480p","bitrate":512000,"fps":15,"height":480,"profile":"VP9Profile0","track_inhibit":"video=<850x480"},{"name":"720p","bitrate":1024000,"fps":25,"height":720,"profile":"VP9Profile0","track_inhibit":"video=<1281x720"}],"track_inhibit":"video=<850x480"},{"process":"Thumbs","track_select":"video=maxbps","track_inhibit":"subtitle=all","inconsequential":true,"exit_unmask":true}]')

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
    tier_level = EXCLUDED.tier_level,
    is_enterprise = EXCLUDED.is_enterprise,
    is_default_prepaid = EXCLUDED.is_default_prepaid,
    is_default_postpaid = EXCLUDED.is_default_postpaid,
    processes_live = EXCLUDED.processes_live,
    processes_vod = EXCLUDED.processes_vod;

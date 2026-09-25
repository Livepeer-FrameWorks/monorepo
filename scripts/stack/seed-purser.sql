-- Stack-only Purser overrides for the demo tenant's tier (developer), applied by
-- scripts/stack/up.sh after the demo seed. psql variable :live carries the
-- canonical catalog's standard_live process config (billing_tiers.yaml), so live
-- ABR goes through the Livepeer ladder exactly as a production tier does; the
-- dev demo seed keeps local-only live processing on purpose.
-- psql variable :window is the tier's DVR window (STACK_DVR_WINDOW_SECONDS,
-- default 120 s), short enough for a window-sized chapter to close within one
-- run; up.sh and the scenarios read the same variable.

UPDATE purser.billing_tiers
SET processes_live = :'live'::jsonb
WHERE tier_name = 'developer';

INSERT INTO purser.tier_entitlements (tier_id, key, value)
SELECT bt.id, v.key, to_jsonb(v.value)
FROM purser.billing_tiers bt
JOIN (VALUES
    ('developer', 'dvr_default_window_seconds', :window),
    ('developer', 'dvr_max_window_seconds', :window)
) AS v(tier_name, key, value) ON v.tier_name = bt.tier_name
ON CONFLICT (tier_id, key) DO UPDATE SET value = EXCLUDED.value;

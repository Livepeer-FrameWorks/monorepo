-- Upgrade existing installations with the two newly explicit DNS entitlement
-- keys before Purser's startup sweep can materialize them into Quartermaster.
-- Fresh installations receive the same values from the embedded catalog via
-- `purser bootstrap`; ON CONFLICT keeps this migration idempotent when that
-- reconcile has already run.
INSERT INTO purser.tier_entitlements (tier_id, key, value)
SELECT tier.id,
       entitlement.key,
       to_jsonb(entitlement.enabled)
FROM purser.billing_tiers AS tier
CROSS JOIN LATERAL (
    VALUES
        ('custom_subdomain_enabled'::varchar(64), tier.tier_name IN ('supporter', 'developer', 'production', 'enterprise')),
        ('custom_domain_enabled'::varchar(64), tier.tier_name IN ('supporter', 'developer', 'production', 'enterprise'))
) AS entitlement(key, enabled)
WHERE tier.tier_name IN ('payg', 'free', 'supporter', 'developer', 'production', 'enterprise')
ON CONFLICT (tier_id, key) DO NOTHING;

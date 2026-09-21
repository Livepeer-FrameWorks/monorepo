-- Tier features and subscription custom features carry only support_level,
-- sla, and processing_customizable (models.BillingFeatures). recording,
-- analytics, api_access, and custom_branding gated nothing and are removed.
-- Only rows that still carry one of those keys are updated: the billing tier
-- update fires the media-authority refresh trigger once per tenant on that
-- tier, and a replay matches no rows.
UPDATE purser.billing_tiers
SET features = features - ARRAY['recording', 'analytics', 'api_access', 'custom_branding'],
    updated_at = NOW()
WHERE features ?| ARRAY['recording', 'analytics', 'api_access', 'custom_branding'];

UPDATE purser.tenant_subscriptions
SET custom_features = custom_features - ARRAY['recording', 'analytics', 'api_access', 'custom_branding'],
    updated_at = NOW()
WHERE custom_features ?| ARRAY['recording', 'analytics', 'api_access', 'custom_branding'];

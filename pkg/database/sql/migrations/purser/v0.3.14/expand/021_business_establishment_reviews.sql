CREATE TABLE IF NOT EXISTS purser.business_establishment_reviews (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id UUID NOT NULL,
    revision BIGINT NOT NULL CHECK (revision > 0),
    UNIQUE (tenant_id, revision),
    profile_sha256 VARCHAR(64),
    is_revocation BOOLEAN NOT NULL DEFAULT FALSE,
    country VARCHAR(2),
    reference TEXT NOT NULL CHECK (length(btrim(reference)) BETWEEN 1 AND 1024),
    reviewed_by UUID NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    valid_until TIMESTAMPTZ NOT NULL,
    retention_until TIMESTAMPTZ NOT NULL DEFAULT (NOW() + INTERVAL '10 years'),
    CONSTRAINT chk_business_establishment_review_window CHECK (
        (is_revocation AND profile_sha256 IS NULL AND country IS NULL AND valid_until = created_at)
        OR (NOT is_revocation AND profile_sha256 IS NOT NULL AND profile_sha256 ~ '^[0-9a-f]{64}$'
            AND country IS NOT NULL AND length(country)=2
            AND valid_until > created_at AND valid_until <= created_at + INTERVAL '366 days')
    )
);
CREATE INDEX IF NOT EXISTS idx_business_establishment_reviews_profile
ON purser.business_establishment_reviews(tenant_id, profile_sha256, country, revision DESC);
CREATE INDEX IF NOT EXISTS idx_business_establishment_reviews_audit
ON purser.business_establishment_reviews(tenant_id, id);
CREATE INDEX IF NOT EXISTS idx_business_establishment_reviews_latest
ON purser.business_establishment_reviews(tenant_id, revision DESC);
CREATE OR REPLACE FUNCTION purser.prevent_business_establishment_review_change()
RETURNS TRIGGER LANGUAGE plpgsql AS $$
BEGIN
    RAISE EXCEPTION 'business establishment reviews are append-only; record a new review';
END;
$$;
CREATE OR REPLACE TRIGGER business_establishment_reviews_update_guard
    BEFORE UPDATE ON purser.business_establishment_reviews
    FOR EACH ROW EXECUTE FUNCTION purser.prevent_business_establishment_review_change();
CREATE OR REPLACE TRIGGER business_establishment_reviews_retention_guard
    BEFORE DELETE ON purser.business_establishment_reviews
    FOR EACH ROW EXECUTE FUNCTION purser.prevent_billing_document_early_delete();

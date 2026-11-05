-- name: GetVATValidationAtQuote :one
SELECT evidence.valid, to_jsonb(evidence)::jsonb AS validation_snapshot,
       (evidence.expires_at > sqlc.arg(quoted_at)::timestamptz)::boolean AS fresh_at_quote
FROM purser.vat_validation_evidence evidence
WHERE evidence.tenant_id = sqlc.arg(tenant_id)::text::uuid
  AND evidence.vat_number_hash = sqlc.arg(vat_number_hash)
  AND evidence.checked_at <= sqlc.arg(quoted_at)::timestamptz
ORDER BY evidence.checked_at DESC, evidence.id DESC
LIMIT 1;

-- name: LockBusinessEvidenceProfile :one
SELECT COALESCE(billing_email, '')::text AS billing_email,
       COALESCE(billing_name, '')::text AS billing_name,
       COALESCE(billing_company, '')::text AS billing_company,
       COALESCE(tax_id, '')::text AS tax_id,
       COALESCE(billing_address, '{}'::jsonb) AS billing_address,
       billing_customer_type::text AS customer_type, COALESCE(status,'')::text AS status
FROM purser.tenant_subscriptions
WHERE tenant_id = sqlc.arg(tenant_id)::text::uuid
ORDER BY created_at DESC LIMIT 1 FOR UPDATE;

-- name: RecordBusinessEstablishmentReview :one
INSERT INTO purser.business_establishment_reviews (
    tenant_id, revision, profile_sha256, country, reference, reviewed_by, valid_until, is_revocation
) VALUES (
    sqlc.arg(tenant_id)::text::uuid,
    (SELECT COALESCE(MAX(previous.revision),0)+1 FROM purser.business_establishment_reviews previous WHERE previous.tenant_id=sqlc.arg(tenant_id)::text::uuid),
    CASE WHEN sqlc.arg(is_revocation)::boolean THEN NULL ELSE sqlc.arg(profile_sha256)::text END,
    CASE WHEN sqlc.arg(is_revocation)::boolean THEN NULL ELSE sqlc.arg(country)::text END,
    sqlc.arg(reference), sqlc.arg(reviewed_by)::text::uuid,
    CASE WHEN sqlc.arg(is_revocation)::boolean THEN NOW() ELSE sqlc.arg(valid_until)::timestamptz END,
    sqlc.arg(is_revocation)::boolean
)
RETURNING id::text AS id, created_at, valid_until, revision;

-- name: GetBusinessEstablishmentReviewSnapshot :one
WITH latest AS (
    SELECT review.*
    FROM purser.business_establishment_reviews review
    WHERE review.tenant_id = sqlc.arg(tenant_id)::text::uuid
      AND ((review.profile_sha256 = sqlc.arg(profile_sha256) AND review.country = sqlc.arg(country))
           OR review.is_revocation = true)
    ORDER BY review.revision DESC LIMIT 1
)
SELECT to_jsonb(latest)::jsonb AS review_snapshot
FROM latest
WHERE latest.is_revocation = false
  AND latest.valid_until > sqlc.arg(valid_at)::timestamptz;

-- name: GetBusinessEvidenceRevision :one
SELECT COALESCE(MAX(review.revision),0)::bigint AS revision
FROM purser.business_establishment_reviews review
WHERE review.tenant_id=sqlc.arg(tenant_id)::text::uuid;

-- name: RetainProviderTaxExpectation :exec
INSERT INTO purser.provider_tax_assessments(tenant_id,profile_sha256,assessed_at,assessment)
SELECT sqlc.arg(tenant_id)::text::uuid,sqlc.arg(profile_sha256)::text,
 sqlc.arg(assessed_at)::timestamptz,sqlc.arg(assessment)::jsonb
WHERE (SELECT accepted.profile_sha256 FROM purser.provider_tax_assessments accepted
       WHERE accepted.tenant_id=sqlc.arg(tenant_id)::text::uuid
       ORDER BY accepted.assessed_at DESC,accepted.id DESC LIMIT 1)
 IS DISTINCT FROM sqlc.arg(profile_sha256)::text;

-- name: GetProviderTaxExpectationAtPoint :one
SELECT assessment FROM purser.provider_tax_assessments
WHERE tenant_id=sqlc.arg(tenant_id)::text::uuid
 AND assessed_at<=sqlc.arg(tax_point)::timestamptz
ORDER BY assessed_at DESC,id DESC LIMIT 1;

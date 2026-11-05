-- name: GetCachedVATValidation :one
WITH latest AS (
    SELECT evidence.* FROM purser.vat_validation_evidence evidence
    WHERE evidence.tenant_id = sqlc.arg(tenant_id)::text::uuid
      AND evidence.vat_number_hash = sqlc.arg(vat_number_hash)
    ORDER BY evidence.checked_at DESC, evidence.id DESC LIMIT 1
)
SELECT latest.valid, to_jsonb(latest)::jsonb AS validation_snapshot,
       (latest.checked_at <= NOW() AND latest.expires_at > NOW())::boolean AS fresh,
       (latest.checked_at <= NOW() AND latest.expires_at >= NOW() - sqlc.arg(outage_grace_hours)::integer * INTERVAL '1 hour')::boolean AS within_outage_grace
FROM latest;

-- name: ListTenantVIESRevalidations :many
SELECT id::text AS id, evidence_json
FROM purser.crypto_accounting_anomalies
WHERE tenant_id=sqlc.arg(tenant_id)::text::uuid
  AND kind='vies_revalidation' AND status='open'
  AND last_seen_at < NOW()-INTERVAL '5 minutes'
  AND NOT (evidence_json ? 'revalidation_outcome')
ORDER BY last_seen_at,id LIMIT 20;

-- name: RecordVIESRevalidationResult :exec
UPDATE purser.crypto_accounting_anomalies
SET last_seen_at=NOW(),
    evidence_json=evidence_json || sqlc.arg(result)::jsonb,
    status=CASE WHEN sqlc.arg(verified)::boolean THEN 'resolved' ELSE status END,
    resolved_at=CASE WHEN sqlc.arg(verified)::boolean THEN NOW() ELSE resolved_at END,
    resolution_note=CASE WHEN sqlc.arg(verified)::boolean THEN 'VIES revalidated retained outage evidence' ELSE resolution_note END
WHERE tenant_id=sqlc.arg(tenant_id)::text::uuid AND id=sqlc.arg(id)::text::uuid
  AND kind='vies_revalidation' AND status='open';

-- name: UpsertVATValidationEvidence :one
INSERT INTO purser.vat_validation_evidence AS evidence (
    tenant_id, country_code, vat_number_hash, vat_number_masked,
    valid, request_date, trader_name, trader_address,
    checked_at, expires_at, raw_response
) VALUES (
    sqlc.arg(tenant_id)::text::uuid, sqlc.arg(country_code),
    sqlc.arg(vat_number_hash), sqlc.arg(vat_number_masked),
    sqlc.arg(valid), sqlc.arg(request_date), sqlc.arg(trader_name),
    sqlc.arg(trader_address), NOW(), NOW() + INTERVAL '24 hours',
    sqlc.arg(raw_response)::jsonb
)
ON CONFLICT (tenant_id, vat_number_hash, request_date) DO UPDATE
SET valid = EXCLUDED.valid,
    trader_name = EXCLUDED.trader_name,
    trader_address = EXCLUDED.trader_address,
    checked_at = NOW(),
    expires_at = NOW() + INTERVAL '24 hours',
    raw_response = EXCLUDED.raw_response
RETURNING to_jsonb(evidence)::jsonb AS validation_snapshot;

-- name: RecordPermanentVIESFault :exec
INSERT INTO purser.vat_validation_evidence(tenant_id,country_code,vat_number_hash,vat_number_masked,valid,request_date,checked_at,expires_at,raw_response)
VALUES(sqlc.arg(tenant_id)::text::uuid,sqlc.arg(country_code),sqlc.arg(vat_number_hash),sqlc.arg(vat_number_masked),false,CURRENT_DATE,NOW(),NOW(),sqlc.arg(raw_response)::jsonb)
ON CONFLICT(tenant_id,vat_number_hash,request_date) DO UPDATE
SET valid=false,checked_at=NOW(),expires_at=NOW(),raw_response=EXCLUDED.raw_response;

-- name: GetActiveTenantBillingModel :one
SELECT COALESCE(billing_model, 'postpaid') AS billing_model
FROM purser.tenant_subscriptions
WHERE tenant_id = sqlc.arg(tenant_id)::text::uuid
  AND status != 'cancelled'
ORDER BY created_at DESC
LIMIT 1;

-- name: SuspendActiveTenantSubscriptions :execrows
UPDATE purser.tenant_subscriptions
SET status = 'suspended', updated_at = NOW()
WHERE tenant_id = sqlc.arg(tenant_id)::text::uuid
  AND status = 'active';

-- name: GetTenantBillingEmail :one
SELECT billing_email
FROM purser.tenant_subscriptions
WHERE tenant_id = sqlc.arg(tenant_id)::text::uuid
ORDER BY created_at DESC
LIMIT 1;

-- name: ExpireX402RateLimitWindows :exec
DELETE FROM purser.x402_rate_limit_windows
WHERE window_started_at < NOW() - INTERVAL '24 hours';

-- name: ConsumeX402RateLimit :one
INSERT INTO purser.x402_rate_limit_windows (
    scope, identity_hash, window_started_at, request_count
) VALUES (
    sqlc.arg(scope), sqlc.arg(identity_hash), date_trunc('minute', NOW()), 1
)
ON CONFLICT (scope, identity_hash, window_started_at) DO UPDATE
SET request_count = purser.x402_rate_limit_windows.request_count + 1,
    updated_at = NOW()
RETURNING request_count;

-- name: GetAutomaticTopupContext :one
SELECT jsonb_build_object(
 'profile',jsonb_build_object('currency',s.presentment_currency,'name',s.billing_name,'company',s.billing_company,'address',s.billing_address,'vat',s.tax_id,'email',s.billing_email,'customer',s.stripe_customer_id),
 'customer_id',COALESCE(s.stripe_customer_id,''),'currency',s.presentment_currency,
 'eligible',s.status='active' AND s.automatic_prepaid_usage AND (s.billing_model='prepaid' OR t.tier_name='free')
 AND s.stripe_subscription_id IS NULL AND s.mollie_subscription_id IS NULL
 AND NOT s.free_fallback_provider_stopping
 AND s.pending_tier_id IS NULL
 AND NOT EXISTS(SELECT 1 FROM purser.pending_topups p WHERE p.tenant_id=s.tenant_id AND p.status='pending' AND NOT EXISTS(SELECT 1 FROM purser.automatic_topup_attempts a WHERE a.tenant_id=s.tenant_id AND a.topup_id=p.id))
 AND NOT EXISTS(SELECT 1 FROM purser.tenant_billing_closures c WHERE c.tenant_id=s.tenant_id)
 AND EXISTS(SELECT 1 FROM purser.pending_topups p WHERE p.tenant_id=s.tenant_id AND p.provider='stripe' AND p.status='completed'),
 'balance_cents',COALESCE((SELECT b.balance_cents FROM purser.prepaid_balances b WHERE b.tenant_id=s.tenant_id AND b.currency='EUR'),0)
)::jsonb AS context
FROM purser.tenant_subscriptions s JOIN purser.billing_tiers t ON t.id=s.tier_id
WHERE s.tenant_id=sqlc.arg(tenant_id)::text::uuid;

-- name: GetAutomaticTopupSettings :one
SELECT tenant_id::text,enabled,threshold_eur_cents,net_amount_minor,currency::text,monthly_gross_limit_minor,consent_version,consent_actor,consent_at,revision::text,stripe_customer_id,stripe_payment_method_id,profile_snapshot,COALESCE(paused_reason,'')::text AS paused_reason
FROM purser.automatic_topup_settings WHERE tenant_id=sqlc.arg(tenant_id)::text::uuid;

-- name: LockAutomaticTopupSettings :one
SELECT tenant_id::text,enabled,threshold_eur_cents,net_amount_minor,currency::text,monthly_gross_limit_minor,consent_version,consent_actor,consent_at,revision::text,stripe_customer_id,stripe_payment_method_id,profile_snapshot,COALESCE(paused_reason,'')::text AS paused_reason
FROM purser.automatic_topup_settings WHERE tenant_id=sqlc.arg(tenant_id)::text::uuid FOR UPDATE;

-- name: SetAutomaticTopupSettings :exec
INSERT INTO purser.automatic_topup_settings(tenant_id,enabled,threshold_eur_cents,net_amount_minor,currency,monthly_gross_limit_minor,consent_version,consent_actor,revision,stripe_customer_id,stripe_payment_method_id,profile_snapshot)
VALUES(sqlc.arg(tenant_id)::text::uuid,true,sqlc.arg(threshold_eur_cents),sqlc.arg(net_amount_minor),sqlc.arg(currency),sqlc.arg(monthly_gross_limit_minor),sqlc.arg(consent_version),sqlc.arg(consent_actor),sqlc.arg(revision)::text::uuid,sqlc.arg(customer_id),sqlc.arg(payment_method_id),sqlc.arg(profile)::jsonb)
ON CONFLICT(tenant_id) DO UPDATE SET enabled=true,threshold_eur_cents=EXCLUDED.threshold_eur_cents,net_amount_minor=EXCLUDED.net_amount_minor,currency=EXCLUDED.currency,monthly_gross_limit_minor=EXCLUDED.monthly_gross_limit_minor,consent_version=EXCLUDED.consent_version,consent_actor=EXCLUDED.consent_actor,consent_at=NOW(),revision=EXCLUDED.revision,stripe_customer_id=EXCLUDED.stripe_customer_id,stripe_payment_method_id=EXCLUDED.stripe_payment_method_id,profile_snapshot=EXCLUDED.profile_snapshot,paused_reason=NULL,updated_at=NOW();

-- name: DisableAutomaticTopup :exec
UPDATE purser.automatic_topup_settings SET enabled=false,revision=gen_random_uuid(),consent_actor=sqlc.arg(actor),updated_at=NOW() WHERE tenant_id=sqlc.arg(tenant_id)::text::uuid;

-- name: PauseAutomaticTopup :exec
UPDATE purser.automatic_topup_settings SET paused_reason=sqlc.arg(reason)::text,last_attempt_at=NOW(),updated_at=NOW() WHERE tenant_id=sqlc.arg(tenant_id)::text::uuid AND revision=sqlc.arg(revision)::text::uuid;

-- name: GetAutomaticTopupMonthlySpend :one
SELECT COALESCE(SUM(gross_amount_minor),0)::bigint AS amount_minor,
 COALESCE(bool_or(currency<>sqlc.arg(currency)),false)::boolean AS different_currency
FROM purser.automatic_topup_attempts WHERE tenant_id=sqlc.arg(tenant_id)::text::uuid
AND budget_month=date_trunc('month',NOW() AT TIME ZONE 'UTC')::date AND status NOT IN ('failed','cancelled');

-- name: ListAutomaticTopupCandidates :many
-- Platform worker selects tenant IDs; every tenant mutation rechecks under its own locks.
SELECT s.tenant_id::text FROM purser.automatic_topup_settings s
JOIN purser.prepaid_balances b ON b.tenant_id=s.tenant_id AND b.currency='EUR'
WHERE s.enabled AND s.paused_reason IS NULL AND b.balance_cents<=s.threshold_eur_cents
AND (s.last_attempt_at IS NULL OR s.last_attempt_at<NOW()-INTERVAL '5 minutes')
AND NOT EXISTS(SELECT 1 FROM purser.automatic_topup_attempts a WHERE a.tenant_id=s.tenant_id AND a.status IN ('reserved','submitted','needs_review'))
ORDER BY s.last_attempt_at ASC NULLS FIRST,s.tenant_id LIMIT 20;

-- name: InsertAutomaticTopupAttempt :exec
INSERT INTO purser.automatic_topup_attempts(id,tenant_id,settings_revision,topup_id,gross_amount_minor,currency,budget_month,customer_id,payment_method_id,profile_snapshot,consent_snapshot)
VALUES(sqlc.arg(attempt_id)::text::uuid,sqlc.arg(tenant_id)::text::uuid,sqlc.arg(revision)::text::uuid,sqlc.arg(topup_id)::text::uuid,sqlc.arg(gross_amount_minor),sqlc.arg(currency),date_trunc('month',NOW() AT TIME ZONE 'UTC')::date,sqlc.arg(customer_id),sqlc.arg(payment_method_id),sqlc.arg(profile)::jsonb,sqlc.arg(consent)::jsonb);

-- name: TouchAutomaticTopup :exec
UPDATE purser.automatic_topup_settings SET last_attempt_at=NOW() WHERE tenant_id=sqlc.arg(tenant_id)::text::uuid;

-- name: EnqueueAutomaticTopup :exec
INSERT INTO purser.provider_webhook_inbox(provider,event_key,headers,raw_payload,next_attempt_at)
VALUES('automatic_topup',sqlc.arg(attempt_id)::text,jsonb_build_object('tenant_id',sqlc.arg(tenant_id)::text,'reference_id',sqlc.arg(attempt_id)::text,'reference_kind','automatic_topup'),sqlc.arg(payload)::bytea,NOW()) ON CONFLICT(provider,event_key) DO NOTHING;

-- name: GetAutomaticTopupAttempt :one
SELECT id::text,tenant_id::text,settings_revision::text,topup_id::text,gross_amount_minor,currency::text,customer_id,payment_method_id,profile_snapshot,status,COALESCE(provider_payment_id,'')::text AS provider_payment_id,created_at
FROM purser.automatic_topup_attempts WHERE tenant_id=sqlc.arg(tenant_id)::text::uuid AND id=sqlc.arg(attempt_id)::text::uuid FOR UPDATE;

-- name: UpdateAutomaticTopupAttempt :exec
UPDATE purser.automatic_topup_attempts SET status=sqlc.arg(status),provider_payment_id=NULLIF(sqlc.arg(provider_payment_id),''),updated_at=NOW()
WHERE tenant_id=sqlc.arg(tenant_id)::text::uuid AND id=sqlc.arg(attempt_id)::text::uuid AND status NOT IN ('settled','failed','cancelled');

-- name: FindAutomaticTopupForReceipt :one
SELECT id::text,topup_id::text,gross_amount_minor,currency::text,customer_id,payment_method_id,status,COALESCE(provider_payment_id,'')::text AS provider_payment_id
FROM purser.automatic_topup_attempts WHERE tenant_id=sqlc.arg(tenant_id)::text::uuid AND topup_id=sqlc.arg(topup_id)::text::uuid;

-- name: CancelAutomaticTopupPendingFunding :exec
WITH cancelled AS (
 UPDATE purser.pending_topups SET status='failed',updated_at=NOW()
 WHERE tenant_id=sqlc.arg(tenant_id)::text::uuid AND id=sqlc.arg(topup_id)::text::uuid AND status='pending'
 RETURNING intent_id
)
UPDATE purser.payment_provider_intents intent SET status='cancelled',updated_at=NOW()
FROM cancelled WHERE intent.id=cancelled.intent_id AND intent.tenant_id=sqlc.arg(tenant_id)::text::uuid
AND intent.status NOT IN ('succeeded','cancelled','terminal_failed');

-- name: HasAutomaticTopupReconciliationHold :one
SELECT EXISTS(SELECT 1 FROM purser.automatic_topup_attempts WHERE tenant_id=sqlc.arg(tenant_id)::text::uuid AND status='needs_review')::boolean AS held;

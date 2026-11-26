-- name: InitializeExhaustedPaymentGrace :exec
-- Starts a missed legacy zero-crossing through the same wallet trigger.
UPDATE purser.prepaid_balances SET balance_cents=balance_cents
WHERE tenant_id=sqlc.arg(tenant_id)::text::uuid AND currency='EUR' AND balance_cents<=0;

-- name: GetPrepaidPaymentGrace :one
SELECT grace_started_at,grace_ends_at,grace_credit_cents,grace_recovered_at,
       grace_duration_seconds,grace_limit_cents,grace_rearm_seconds,
       free_recovery_started_at,free_recovery_until
FROM purser.prepaid_balances WHERE tenant_id=sqlc.arg(tenant_id)::text::uuid AND currency='EUR';

-- name: BeginFreeMediaRecovery :exec
-- A token funding interval does not renew an existing recovery window.
UPDATE purser.prepaid_balances
SET free_recovery_media_cutoff_at=NOW(),
    free_recovery_started_at=COALESCE(free_recovery_started_at,NOW()),
    free_recovery_until=COALESCE(free_recovery_until,NOW()+INTERVAL '30 days')
WHERE tenant_id=sqlc.arg(tenant_id)::text::uuid AND currency='EUR';

-- name: ListPrepaidPaymentNotices :many
SELECT tenant_id::text AS tenant_id,recipient,tenant_name,kind,balance_cents,grace_ends_at,grace_credit_cents,free_recovery_until
FROM purser.prepaid_payment_notice_candidates
WHERE kind<>'' AND (payment_notice_attempt_at IS NULL OR payment_notice_attempt_at<NOW()-INTERVAL '5 minutes')
ORDER BY payment_notice_attempt_at ASC NULLS FIRST,tenant_id LIMIT 20;

-- name: ClaimPrepaidPaymentNotice :execrows
UPDATE purser.prepaid_balances b SET payment_notice_attempt_at=NOW(),payment_notice_error=NULL
FROM purser.prepaid_payment_notice_candidates n
WHERE b.tenant_id=sqlc.arg(tenant_id)::text::uuid AND b.currency='EUR' AND n.tenant_id=b.tenant_id
 AND n.kind=sqlc.arg(kind)::text
 AND b.grace_ends_at IS NOT DISTINCT FROM sqlc.narg(grace_ends_at)::timestamptz
 AND b.free_recovery_until IS NOT DISTINCT FROM sqlc.narg(free_recovery_until)::timestamptz
 AND (b.payment_notice_attempt_at IS NULL OR b.payment_notice_attempt_at<NOW()-INTERVAL '5 minutes');

-- name: FinishPrepaidPaymentNotice :execrows
UPDATE purser.prepaid_balances SET
 low_notice_at=CASE WHEN sqlc.arg(kind)::text='low' AND sqlc.arg(sent)::boolean THEN NOW() ELSE low_notice_at END,
 grace_notice_at=CASE WHEN sqlc.arg(kind)::text='grace' AND sqlc.arg(sent)::boolean THEN NOW() ELSE grace_notice_at END,
 grace_reminder_at=CASE WHEN sqlc.arg(kind)::text='grace_reminder' AND sqlc.arg(sent)::boolean THEN NOW() ELSE grace_reminder_at END,
 recovery_notice_at=CASE WHEN sqlc.arg(kind)::text='recovery' AND sqlc.arg(sent)::boolean THEN NOW() ELSE recovery_notice_at END,
 recovery_reminder_at=CASE WHEN sqlc.arg(kind)::text='recovery_reminder' AND sqlc.arg(sent)::boolean THEN NOW() ELSE recovery_reminder_at END,
 payment_notice_error=sqlc.narg(last_error)::text
WHERE tenant_id=sqlc.arg(tenant_id)::text::uuid AND currency='EUR'
 AND grace_ends_at IS NOT DISTINCT FROM sqlc.narg(grace_ends_at)::timestamptz
 AND free_recovery_until IS NOT DISTINCT FROM sqlc.narg(free_recovery_until)::timestamptz;

-- name: GetMediaRecoverySnapshot :one
SELECT s.id::text AS subscription_id,s.tier_id::text AS tier_id,s.billing_model,s.status,s.automatic_prepaid_usage,t.tier_name,
 b.balance_cents,b.free_recovery_media_cutoff_at AS cutoff_at,b.free_recovery_until,
 b.recovery_notice_at,b.recovery_reminder_at,(s.pending_tier_id IS NOT NULL)::boolean AS pending_purchase,
 NOW()::timestamptz AS database_now,
 COALESCE((SELECT MAX(e.cutoff_at) FROM purser.media_recovery_expiries e WHERE e.tenant_id=s.tenant_id),TIMESTAMPTZ 'epoch')::timestamptz AS committed_cutoff_at
FROM purser.tenant_subscriptions s JOIN purser.billing_tiers t ON t.id=s.tier_id
JOIN purser.prepaid_balances b ON b.tenant_id=s.tenant_id AND b.currency='EUR'
WHERE s.tenant_id=sqlc.arg(tenant_id)::text::uuid;

-- name: InsertMediaRecoveryExpiry :exec
INSERT INTO purser.media_recovery_expiries(tenant_id,cutoff_at,recovery_until)
VALUES(sqlc.arg(tenant_id)::text::uuid,sqlc.arg(cutoff_at)::timestamptz,sqlc.arg(recovery_until)::timestamptz)
ON CONFLICT(tenant_id,cutoff_at)DO NOTHING;

-- name: GetLargestSettledPrepaidTopup :one
-- Uses the same bounded funding predicate as the wallet grace trigger.
SELECT COALESCE(MAX(amount_cents),0)::bigint AS amount_cents
FROM purser.balance_transactions
WHERE tenant_id=sqlc.arg(tenant_id)::text::uuid AND transaction_type='topup' AND amount_cents>0;

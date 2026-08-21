-- name: EnqueueStripeMeterEvents :exec
INSERT INTO purser.stripe_meter_events_outbox (
    tenant_id, cluster_id, meter, stripe_meter_event_name, quantity,
    dimensions, period_start, period_end, invoice_id, invoice_line_item_id
)
SELECT
    sqlc.arg(tenant_id)::uuid,
    COALESCE(li.cluster_id, ''),
    li.meter,
    route.stripe_meter_event_name,
    li.quantity,
    li.dimensions,
    inv.period_start,
    inv.period_end,
    inv.id,
    li.id
FROM purser.invoice_line_items li
JOIN purser.billing_invoices inv ON inv.id = li.invoice_id
LEFT JOIN purser.cluster_pricing cp ON cp.cluster_id = li.cluster_id
CROSS JOIN LATERAL (
    SELECT CASE li.pricing_source
        WHEN 'cluster_metered' THEN COALESCE(cp.metered_rates -> li.meter ->> 'stripe_meter_event_name', cp.stripe_meter_event_name)
        WHEN 'cluster_custom' THEN COALESCE(cp.metered_rates -> li.meter ->> 'stripe_meter_event_name', cp.stripe_meter_event_name)
        ELSE NULL
    END AS stripe_meter_event_name
) route
WHERE li.invoice_id = sqlc.arg(invoice_id)::uuid
  AND li.tenant_id = sqlc.arg(tenant_id)::uuid
  AND li.amount > 0
  AND NULLIF(li.meter, '') IS NOT NULL
  AND NULLIF(route.stripe_meter_event_name, '') IS NOT NULL
ON CONFLICT (invoice_line_item_id, stripe_meter_event_name) DO NOTHING;

-- name: ResolveActiveStripeCustomer :one
SELECT stripe_customer_id
FROM purser.tenant_subscriptions
WHERE tenant_id = $1
  AND status = 'active'
ORDER BY created_at DESC
LIMIT 1;

-- name: ListPendingStripeMeterEvents :many
SELECT id, tenant_id, cluster_id, meter, stripe_meter_event_name,
       quantity::text, dimensions, period_start, attempt_count
FROM purser.stripe_meter_events_outbox
WHERE sent_at IS NULL
  AND attempt_count < sqlc.arg(max_attempts)::integer
ORDER BY created_at
LIMIT sqlc.arg(batch_size)::integer;

-- name: MarkStripeMeterEventSent :exec
UPDATE purser.stripe_meter_events_outbox
SET sent_at = NOW(), updated_at = NOW(), last_error = NULL
WHERE id = $1;

-- name: RecordStripeMeterEventFailure :exec
UPDATE purser.stripe_meter_events_outbox
SET attempt_count = attempt_count + 1,
    last_error = sqlc.arg(last_error),
    updated_at = NOW()
WHERE id = sqlc.arg(id);

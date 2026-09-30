-- name: CollectPostpaidInvoiceUsage :many
-- The usage a postpaid invoice of [window_start, window_end) rates. A usage
-- record a prepaid usage settlement paid from the balance belongs to the
-- prepaid statement, never to an invoice. A period that starts at a switch
-- from prepaid also rates the records of the prepaid phase before it that
-- reached Purser after the switch, which the balance never paid:
-- unsettled_from is where that phase began, or window_start otherwise.
WITH params AS (
    SELECT sqlc.arg(tenant_id)::text::uuid AS tenant_id,
           sqlc.arg(window_start)::timestamptz AS window_start,
           sqlc.arg(window_end)::timestamptz AS window_end,
           LEAST(sqlc.arg(unsettled_from)::timestamptz, sqlc.arg(window_start)::timestamptz) AS unsettled_from
), usage_rows AS (
    SELECT COALESCE(ur.cluster_id, '') AS cluster_id, ur.usage_type, ur.usage_value
    FROM purser.usage_records ur CROSS JOIN params p
    WHERE ur.tenant_id = p.tenant_id
      AND ur.period_start < p.window_end
      AND ur.period_end > p.unsettled_from
      AND (ur.period_end > p.window_start OR ur.created_at >= p.window_start)
      AND ur.usage_type NOT IN ('unique_users', 'total_streams', 'total_viewers', 'unique_users_period')
      AND ur.value_kind = 'delta'
      AND ur.granularity = 'minute_5'
      AND NOT EXISTS (
          SELECT 1 FROM purser.prepaid_usage_settlements settlement
          WHERE settlement.report_id = ur.report_id AND settlement.tenant_id = ur.tenant_id
      )
    UNION ALL
    SELECT COALESCE(ua.cluster_id, '') AS cluster_id, ua.usage_type, ua.delta_value AS usage_value
    FROM purser.usage_adjustments ua CROSS JOIN params p
    WHERE ua.tenant_id = p.tenant_id
      AND ua.period_start < p.window_end
      AND ua.period_end > p.window_start
      AND ua.status = 'applied'
      AND ua.value_kind = 'correction_delta'
      AND ua.usage_type NOT IN ('unique_users', 'total_streams', 'total_viewers', 'unique_users_period')
)
SELECT cluster_id, usage_type,
       (CASE WHEN usage_type IN ('peak_bandwidth_mbps', 'max_viewers')
             THEN MAX(usage_value) ELSE SUM(usage_value) END)::float8 AS aggregated_value
FROM usage_rows
GROUP BY cluster_id, usage_type;

-- name: CollectPostpaidInvoiceDimensionedUsage :many
-- CollectPostpaidInvoiceUsage per meter dimension.
WITH params AS (
    SELECT sqlc.arg(tenant_id)::text::uuid AS tenant_id,
           sqlc.arg(window_start)::timestamptz AS window_start,
           sqlc.arg(window_end)::timestamptz AS window_end,
           LEAST(sqlc.arg(unsettled_from)::timestamptz, sqlc.arg(window_start)::timestamptz) AS unsettled_from
), dimensioned_rows AS (
    SELECT ur.cluster_id, ur.usage_type, ur.unit, ur.dimensions, ur.usage_value
    FROM purser.usage_records ur CROSS JOIN params p
    WHERE ur.tenant_id = p.tenant_id
      AND ur.period_start < p.window_end
      AND ur.period_end > p.unsettled_from
      AND (ur.period_end > p.window_start OR ur.created_at >= p.window_start)
      AND ur.value_kind = 'delta' AND ur.granularity = 'minute_5'
      AND NOT EXISTS (
          SELECT 1 FROM purser.prepaid_usage_settlements settlement
          WHERE settlement.report_id = ur.report_id AND settlement.tenant_id = ur.tenant_id
      )
    UNION ALL
    SELECT ua.cluster_id, ua.usage_type, ua.unit, ua.dimensions, ua.delta_value
    FROM purser.usage_adjustments ua CROSS JOIN params p
    WHERE ua.tenant_id = p.tenant_id
      AND ua.period_start < p.window_end
      AND ua.period_end > p.window_start
      AND ua.status = 'applied' AND ua.value_kind = 'correction_delta'
)
SELECT COALESCE(r.cluster_id, '') AS cluster_id,
       r.usage_type, r.unit, r.dimensions,
       (CASE WHEN d.aggregation = 'max' THEN MAX(r.usage_value)
             ELSE SUM(r.usage_value) END)::float8 AS quantity
FROM dimensioned_rows r
JOIN purser.meter_definitions d ON d.meter = r.usage_type AND d.active = TRUE
GROUP BY r.cluster_id, r.usage_type, r.unit, r.dimensions, d.aggregation;

-- name: GetClosedPrepaidPhaseStart :one
-- Where the prepaid phase began that a switch to postpaid closed at
-- period_start, from the statement that closed it.
SELECT period_start::timestamptz AS phase_start
FROM purser.billing_invoices
WHERE tenant_id = sqlc.arg(tenant_id)::text::uuid
  AND document_kind = 'prepaid_statement'
  AND period_end = sqlc.arg(period_start)::timestamptz
  AND period_start < sqlc.arg(period_start)::timestamptz
  AND usage_details->'statement'->>'closes_prepaid_phase' = 'true'
ORDER BY period_start
LIMIT 1;

-- name: InsertPrepaidStatement :one
-- A statement is finalized when it is written: nothing is due, so it is paid.
-- It is stated in EUR, the currency of the prepaid balance.
INSERT INTO purser.billing_invoices (
    tenant_id, invoice_number, document_kind, status, currency, amount, due_date, paid_at,
    base_amount, metered_amount, gross_metered_amount, prepaid_credit_applied,
    usage_details, period_start, period_end,
    presentment_amount_cents, presentment_currency, presentment_units_per_eur,
    presentment_reference_date, finalized_at, created_at, updated_at
) VALUES (
    sqlc.arg(tenant_id)::text::uuid,
    'STM-' || LPAD(nextval('purser.billing_invoice_number_seq')::text, 10, '0'),
    'prepaid_statement', 'paid', 'EUR', 0,
    sqlc.arg(finalized_at)::timestamptz, sqlc.arg(finalized_at)::timestamptz,
    sqlc.arg(base_amount)::text::numeric, sqlc.arg(metered_amount)::text::numeric,
    sqlc.arg(gross_metered_amount)::text::numeric, 0,
    sqlc.arg(usage_details)::jsonb, sqlc.arg(period_start)::timestamptz, sqlc.arg(period_end)::timestamptz,
    0, 'EUR', 1, sqlc.arg(finalized_at)::date, sqlc.arg(finalized_at)::timestamptz, NOW(), NOW()
)
ON CONFLICT (tenant_id, period_start) WHERE period_start IS NOT NULL
DO NOTHING
RETURNING id::text AS id, invoice_number;

-- name: ConvertDraftToPrepaidStatement :one
-- An open draft of the period, left by postpaid billing earlier in the period,
-- becomes the period's statement.
UPDATE purser.billing_invoices
SET invoice_number = 'STM-' || LPAD(nextval('purser.billing_invoice_number_seq')::text, 10, '0'),
    document_kind = 'prepaid_statement',
    status = 'paid',
    currency = 'EUR',
    amount = 0,
    due_date = sqlc.arg(finalized_at)::timestamptz,
    paid_at = sqlc.arg(finalized_at)::timestamptz,
    base_amount = sqlc.arg(base_amount)::text::numeric,
    metered_amount = sqlc.arg(metered_amount)::text::numeric,
    gross_metered_amount = sqlc.arg(gross_metered_amount)::text::numeric,
    prepaid_credit_applied = 0,
    usage_details = sqlc.arg(usage_details)::jsonb,
    period_end = sqlc.arg(period_end)::timestamptz,
    presentment_amount_cents = 0,
    presentment_currency = 'EUR',
    presentment_units_per_eur = 1,
    presentment_reference_date = sqlc.arg(finalized_at)::date,
    finalized_at = sqlc.arg(finalized_at)::timestamptz,
    updated_at = NOW()
WHERE id = sqlc.arg(invoice_id)::text::uuid
  AND tenant_id = sqlc.arg(tenant_id)::text::uuid
  AND status IN ('draft', 'manual_review')
RETURNING id::text AS id, invoice_number;

-- name: SumPrepaidUsageSettlementsForStatement :one
-- What prepaid usage settlements took from the balance for usage of billing
-- periods starting inside [period_start, period_end).
SELECT COALESCE(SUM(amount_micro), 0)::bigint AS amount_micro,
       COUNT(*)::bigint AS settlements
FROM purser.prepaid_usage_settlements
WHERE tenant_id = sqlc.arg(tenant_id)::text::uuid
  AND billing_period_start >= sqlc.arg(period_start)::timestamptz
  AND billing_period_start < sqlc.arg(period_end)::timestamptz;

-- name: GetPrepaidStatementLedger :one
-- Balance movements a statement states, read under the prepaid balance lock:
-- everything posted since the period start and since its end, and the
-- top-ups and usage deductions posted inside the period.
SELECT COALESCE(SUM(amount_cents), 0)::bigint AS since_period_start_cents,
       COALESCE(SUM(amount_cents) FILTER (WHERE created_at >= sqlc.arg(period_end)::timestamptz), 0)::bigint AS since_period_end_cents,
       COALESCE(SUM(amount_cents) FILTER (
           WHERE created_at < sqlc.arg(period_end)::timestamptz AND transaction_type = 'topup'
       ), 0)::bigint AS topup_cents,
       COUNT(*) FILTER (
           WHERE created_at < sqlc.arg(period_end)::timestamptz AND transaction_type = 'topup'
       )::bigint AS topups,
       COALESCE(SUM(amount_cents) FILTER (
           WHERE created_at < sqlc.arg(period_end)::timestamptz AND reference_type = 'usage_summary'
       ), 0)::bigint AS usage_cents
FROM purser.balance_transactions
WHERE tenant_id = sqlc.arg(tenant_id)::text::uuid
  AND created_at >= sqlc.arg(period_start)::timestamptz;

-- name: InsertPrepaidStatementFeeTransaction :exec
-- The period's monthly fees a prepaid statement charged to the balance; the
-- statement is the reference, so the charge is written once.
INSERT INTO purser.balance_transactions (
    tenant_id, amount_cents, balance_after_cents, transaction_type, description,
    reference_id, reference_type, actor_kind, created_at
) VALUES (
    sqlc.arg(tenant_id)::text::uuid, sqlc.arg(amount_cents), sqlc.arg(balance_after_cents),
    'usage', sqlc.arg(description), sqlc.arg(statement_id)::text::uuid, 'prepaid_statement_fees',
    'system', NOW()
);

-- name: EnqueuePrepaidStatementEmail :exec
INSERT INTO purser.invoice_email_outbox
    (invoice_id, tenant_id, recipient, notification_type, reminder_stage)
VALUES (
    sqlc.arg(invoice_id)::text::uuid,
    sqlc.arg(tenant_id)::text::uuid,
    sqlc.arg(recipient),
    'prepaid_statement',
    0
)
ON CONFLICT (invoice_id, notification_type, reminder_stage) DO NOTHING;

-- name: GetPrepaidStatement :one
SELECT invoice_number, status, period_start, period_end,
       metered_amount::float8 AS metered_amount,
       usage_details
FROM purser.billing_invoices
WHERE id = sqlc.arg(invoice_id)::text::uuid
  AND tenant_id = sqlc.arg(tenant_id)::text::uuid
  AND document_kind = 'prepaid_statement';

-- name: GetPrepaidStatementDocument :one
SELECT invoice.invoice_number, invoice.status,
       COALESCE(invoice.created_at, NOW()) AS issued_at, invoice.retention_until,
       invoice.period_start, invoice.period_end,
       invoice.usage_details,
       COALESCE(subscription.billing_name, '')::text AS customer_name,
       COALESCE(subscription.billing_company, '')::text AS customer_company,
       COALESCE(subscription.billing_address::text, '')::text AS customer_address,
       COALESCE(subscription.tax_id, '')::text AS customer_vat
FROM purser.billing_invoices invoice
LEFT JOIN purser.tenant_subscriptions subscription ON subscription.tenant_id = invoice.tenant_id
WHERE invoice.id = sqlc.arg(document_id)::text::uuid
  AND invoice.tenant_id = sqlc.arg(tenant_id)::text::uuid
  AND invoice.document_kind = 'prepaid_statement';

-- name: GetSubscriptionPrepaidPhase :one
-- The subscription facts that decide a prepaid phase and its statement, and
-- the database clock that usage record receipt times are written with.
SELECT billing_model, status, billing_email,
       billing_period_start, billing_period_end,
       stripe_subscription_id, mollie_subscription_id,
       NOW()::timestamptz AS database_now
FROM purser.tenant_subscriptions
WHERE tenant_id = sqlc.arg(tenant_id)::text::uuid;

-- name: StartPostpaidPhase :execrows
-- A switch from prepaid to postpaid closed the prepaid phase at
-- period_start: the postpaid period runs from there.
UPDATE purser.tenant_subscriptions
SET billing_period_start = sqlc.arg(period_start)::timestamp,
    billing_period_end = sqlc.arg(period_end)::timestamp,
    next_billing_date = sqlc.arg(period_end)::timestamp,
    updated_at = NOW()
WHERE tenant_id = sqlc.arg(tenant_id)::text::uuid;

-- name: ListPrepaidDoubleCharges :many
-- Finalized usage invoices whose period holds prepaid usage settlements: the
-- tenant was prepaid during the period, and usage its prepaid balance had
-- already paid was rated again on the invoice. Invoices that leave
-- prepaid-settled usage out record prepaid_settled_usage_excluded and are not
-- listed. usage_amount is the invoice's usage lines without base and monthly
-- cluster fees.
SELECT invoice.id::text AS invoice_id,
       invoice.tenant_id::text AS tenant_id,
       invoice.invoice_number,
       invoice.status,
       invoice.period_start::timestamptz AS period_start,
       invoice.period_end::timestamptz AS period_end,
       invoice.amount::text AS amount,
       invoice.prepaid_credit_applied::text AS prepaid_credit_applied,
       COALESCE(invoice.presentment_currency, invoice.currency)::text AS presentment_currency,
       COALESCE(invoice.presentment_amount_cents, ROUND(invoice.amount * 100)::bigint)::bigint AS presentment_amount_cents,
       settled.amount_micro::bigint AS settled_micro,
       COALESCE(lines.usage_amount, 0)::text AS usage_amount
FROM purser.billing_invoices invoice
JOIN LATERAL (
    SELECT SUM(settlement.amount_micro) AS amount_micro
    FROM purser.prepaid_usage_settlements settlement
    WHERE settlement.tenant_id = invoice.tenant_id
      AND settlement.billing_period_start >= invoice.period_start
      AND settlement.billing_period_start < invoice.period_end
) settled ON settled.amount_micro IS NOT NULL
LEFT JOIN LATERAL (
    SELECT SUM(line.amount) AS usage_amount
    FROM purser.invoice_line_items line
    WHERE line.invoice_id = invoice.id
      AND line.line_key <> 'base_subscription'
      AND line.pricing_source <> 'cluster_monthly'
) lines ON TRUE
WHERE invoice.document_kind = 'invoice'
  AND invoice.period_start IS NOT NULL
  AND invoice.period_end IS NOT NULL
  AND invoice.base_fee_period_start IS NULL
  AND invoice.status NOT IN ('draft', 'manual_review', 'cancelled')
  AND invoice.usage_details->>'prepaid_settled_usage_excluded' IS NULL
  AND (NOT sqlc.arg(filter_tenant)::boolean OR invoice.tenant_id = sqlc.arg(tenant_id)::text::uuid)
ORDER BY invoice.period_start, invoice.tenant_id, invoice.id
LIMIT sqlc.arg(result_limit)::integer;

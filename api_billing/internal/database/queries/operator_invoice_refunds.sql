-- name: GetOperatorInvoiceRefundSource :one
SELECT jsonb_build_object('invoice',to_jsonb(i),'payment',to_jsonb(p),'provider_customer_id',COALESCE(intent.provider_customer_id,''))::jsonb AS source
FROM purser.billing_invoices i
JOIN purser.billing_payments p ON p.tenant_id=i.tenant_id AND p.invoice_id=i.id
LEFT JOIN purser.payment_provider_intents intent ON intent.tenant_id=i.tenant_id AND intent.id=p.intent_id
WHERE i.tenant_id=sqlc.arg(tenant_id)::text::uuid AND i.id=sqlc.arg(invoice_id)::text::uuid
 AND i.status='paid' AND i.document_kind='invoice' AND i.tax_status='ready'
 AND i.source_reference_type IS DISTINCT FROM 'prepaid_topup'
 AND i.customer_snapshot IS NOT NULL AND i.supplier_snapshot IS NOT NULL
 AND i.prepaid_credit_applied=0 AND i.reversed_paid_cents=0
 AND p.method='card' AND p.status='confirmed' AND p.reversed_amount_cents=0
 AND p.original_amount_cents=i.document_gross_minor AND p.original_currency=i.presentment_currency
 AND p.eur_amount_cents=i.gross_eur_cents
 AND (SELECT count(*) FROM purser.billing_payments other WHERE other.tenant_id=i.tenant_id AND other.invoice_id=i.id)=1
 AND NOT EXISTS(SELECT 1 FROM purser.credit_notes n WHERE n.tenant_id=i.tenant_id AND n.source_document_type='invoice' AND n.source_document_id=i.id)
 AND NOT EXISTS(SELECT 1 FROM purser.payment_reversals v WHERE v.tenant_id=i.tenant_id AND (v.payment_id=p.id OR v.invoice_id=i.id))
 AND NOT EXISTS(SELECT 1 FROM purser.balance_transactions b WHERE b.tenant_id=i.tenant_id AND b.transaction_type='operator_adjustment' AND b.amount_cents>0 AND b.created_at>=i.created_at)
FOR UPDATE OF i,p;

-- name: GetOperatorInvoiceRefund :one
SELECT id::text,tenant_id::text,invoice_id::text,payment_id::text,provider,provider_payment_id,amount_minor,currency::text,reason,actor_id,source_snapshot,review_fingerprint,status,COALESCE(provider_refund_id,'')::text AS provider_refund_id,COALESCE(last_error,'')::text AS last_error
FROM purser.operator_invoice_refunds WHERE tenant_id=sqlc.arg(tenant_id)::text::uuid AND id=sqlc.arg(request_id)::text::uuid;

-- name: InsertOperatorInvoiceRefund :exec
INSERT INTO purser.operator_invoice_refunds(id,tenant_id,invoice_id,payment_id,provider,provider_payment_id,amount_minor,currency,reason,actor_id,source_snapshot,review_fingerprint)
VALUES(sqlc.arg(request_id)::text::uuid,sqlc.arg(tenant_id)::text::uuid,sqlc.arg(invoice_id)::text::uuid,sqlc.arg(payment_id)::text::uuid,sqlc.arg(provider),sqlc.arg(provider_payment_id),sqlc.arg(amount_minor),sqlc.arg(currency),sqlc.arg(reason),sqlc.arg(actor_id),sqlc.arg(source_snapshot)::jsonb,sqlc.arg(fingerprint));

-- name: HoldOperatorRefundInvoice :execrows
UPDATE purser.billing_invoices SET status='manual_review',updated_at=NOW() WHERE tenant_id=sqlc.arg(tenant_id)::text::uuid AND id=sqlc.arg(invoice_id)::text::uuid AND status='paid';

-- name: EnqueueOperatorInvoiceRefund :exec
INSERT INTO purser.provider_webhook_inbox(provider,event_key,headers,raw_payload,next_attempt_at)
VALUES('operator_refund',sqlc.arg(request_id)::text,jsonb_build_object('tenant_id',sqlc.arg(tenant_id)::text,'reference_id',sqlc.arg(request_id)::text,'reference_kind','completed_invoice_refund'),sqlc.arg(payload)::bytea,NOW()) ON CONFLICT(provider,event_key) DO NOTHING;

-- name: UpdateOperatorInvoiceRefund :exec
UPDATE purser.operator_invoice_refunds SET status=sqlc.arg(status),provider_refund_id=NULLIF(sqlc.arg(provider_refund_id),''),last_error=NULLIF(sqlc.arg(last_error),''),updated_at=NOW()
WHERE tenant_id=sqlc.arg(tenant_id)::text::uuid AND id=sqlc.arg(request_id)::text::uuid AND status<>'succeeded';

-- name: IssueOperatorRefundCreditNote :exec
INSERT INTO purser.credit_notes(credit_note_number,tenant_id,source_document_type,source_document_id,reversal_reference_type,reversal_reference_id,amount_cents,currency,reason,evidence_json)
SELECT purser.next_billing_document_number('CN'),tenant_id,'invoice',invoice_id,'operator_cash_refund',id::text,amount_minor,currency,reason,jsonb_build_object('provider',provider,'provider_refund_id',provider_refund_id,'actor_id',actor_id)
FROM purser.operator_invoice_refunds WHERE tenant_id=sqlc.arg(tenant_id)::text::uuid AND id=sqlc.arg(request_id)::text::uuid AND status='succeeded'
ON CONFLICT(source_document_type,source_document_id,reversal_reference_type,reversal_reference_id) DO NOTHING;

-- name: FinalizeOperatorRefundInvoice :execrows
UPDATE purser.billing_invoices SET status='refunded',updated_at=NOW()
WHERE tenant_id=sqlc.arg(tenant_id)::text::uuid AND id=sqlc.arg(invoice_id)::text::uuid
AND EXISTS(SELECT 1 FROM purser.operator_invoice_refunds r WHERE r.tenant_id=sqlc.arg(tenant_id)::text::uuid AND r.invoice_id=sqlc.arg(invoice_id)::text::uuid AND r.status='succeeded');

-- name: GetOperatorInvoiceRefundReference :one
SELECT id::text FROM purser.operator_invoice_refunds WHERE tenant_id=sqlc.arg(tenant_id)::text::uuid AND invoice_id=sqlc.arg(invoice_id)::text::uuid;

-- name: RecordOperatorInvoiceRefundError :exec
UPDATE purser.operator_invoice_refunds SET last_error=sqlc.arg(message)::text,updated_at=NOW()
WHERE tenant_id=sqlc.arg(tenant_id)::text::uuid AND id=sqlc.arg(request_id)::text::uuid AND status<>'succeeded';

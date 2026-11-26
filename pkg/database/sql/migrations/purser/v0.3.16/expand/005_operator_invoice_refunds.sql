ALTER TABLE purser.billing_invoices ADD CONSTRAINT chk_billing_invoices_status_refunds CHECK(status IN ('draft','pending','paid','overdue','failed','cancelled','manual_review','refunded')) NOT VALID;
ALTER TABLE purser.billing_invoices DROP CONSTRAINT IF EXISTS chk_billing_invoices_status;
CREATE TABLE IF NOT EXISTS purser.operator_invoice_refunds (
 id UUID PRIMARY KEY,
 tenant_id UUID NOT NULL,
 invoice_id UUID NOT NULL REFERENCES purser.billing_invoices(id),
 payment_id UUID NOT NULL REFERENCES purser.billing_payments(id),
 provider TEXT NOT NULL CHECK(provider IN ('stripe','mollie')),
 provider_payment_id TEXT NOT NULL,
 amount_minor BIGINT NOT NULL CHECK(amount_minor>0),
 currency CHAR(3) NOT NULL CHECK(currency IN ('EUR','USD','GBP')),
 reason TEXT NOT NULL CHECK(length(btrim(reason))>0),
 actor_id TEXT NOT NULL,
 source_snapshot JSONB NOT NULL,
 review_fingerprint TEXT NOT NULL,
 status TEXT NOT NULL DEFAULT 'queued' CHECK(status IN ('queued','submitted','succeeded','needs_review')),
 provider_refund_id TEXT,
 last_error TEXT,
 created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
 updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
 UNIQUE(tenant_id,invoice_id),
 UNIQUE(provider,provider_payment_id)
);
CREATE INDEX IF NOT EXISTS idx_operator_invoice_refunds_tenant ON purser.operator_invoice_refunds(tenant_id,created_at DESC);

CREATE OR REPLACE FUNCTION purser.fence_operator_invoice_refund_collection()
RETURNS TRIGGER LANGUAGE plpgsql AS $$
DECLARE refund_state TEXT;
BEGIN
 SELECT status INTO refund_state FROM purser.operator_invoice_refunds WHERE tenant_id=NEW.tenant_id AND invoice_id=NEW.id;
 IF refund_state='succeeded' THEN NEW.status:='refunded';
 ELSIF refund_state IS NOT NULL THEN NEW.status:='manual_review';
 END IF;
 RETURN NEW;
END;
$$;
CREATE OR REPLACE TRIGGER operator_invoice_refund_collection_fence BEFORE UPDATE ON purser.billing_invoices FOR EACH ROW EXECUTE FUNCTION purser.fence_operator_invoice_refund_collection();

CREATE OR REPLACE FUNCTION purser.fence_operator_invoice_refund_credit()
RETURNS TRIGGER LANGUAGE plpgsql AS $$
BEGIN
 IF NEW.source_document_type='invoice' AND NEW.reversal_reference_type<>'operator_cash_refund'
 AND EXISTS(SELECT 1 FROM purser.operator_invoice_refunds WHERE tenant_id=NEW.tenant_id AND invoice_id=NEW.source_document_id) THEN
 RAISE EXCEPTION 'invoice has an approved full cash refund; reconcile its refund before issuing another credit';
 END IF;
 RETURN NEW;
END;
$$;
CREATE OR REPLACE TRIGGER operator_invoice_refund_credit_fence BEFORE INSERT ON purser.credit_notes FOR EACH ROW EXECUTE FUNCTION purser.fence_operator_invoice_refund_credit();

CREATE OR REPLACE FUNCTION purser.retain_operator_invoice_refund_facts()
RETURNS TRIGGER LANGUAGE plpgsql AS $$
BEGIN
 IF ROW(NEW.id,NEW.tenant_id,NEW.invoice_id,NEW.payment_id,NEW.provider,NEW.provider_payment_id,NEW.amount_minor,NEW.currency,NEW.reason,NEW.actor_id,NEW.source_snapshot,NEW.review_fingerprint)
 IS DISTINCT FROM ROW(OLD.id,OLD.tenant_id,OLD.invoice_id,OLD.payment_id,OLD.provider,OLD.provider_payment_id,OLD.amount_minor,OLD.currency,OLD.reason,OLD.actor_id,OLD.source_snapshot,OLD.review_fingerprint)
 THEN RAISE EXCEPTION 'approved refund facts are immutable'; END IF;
 IF OLD.status='succeeded' AND NEW.status<>'succeeded' THEN RAISE EXCEPTION 'settled refund cannot reopen'; END IF;
 RETURN NEW;
END;
$$;
CREATE OR REPLACE TRIGGER operator_invoice_refund_facts_guard BEFORE UPDATE ON purser.operator_invoice_refunds FOR EACH ROW EXECUTE FUNCTION purser.retain_operator_invoice_refund_facts();

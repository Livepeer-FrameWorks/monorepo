ALTER TABLE purser.pending_topups
ADD COLUMN IF NOT EXISTS net_eur_cents BIGINT,
ADD COLUMN IF NOT EXISTS vat_eur_cents BIGINT,
ADD COLUMN IF NOT EXISTS gross_eur_cents BIGINT,
ADD COLUMN IF NOT EXISTS tax_assessment JSONB,
ADD COLUMN IF NOT EXISTS tax_document_id UUID;
ALTER TABLE purser.billing_invoices
ADD COLUMN IF NOT EXISTS source_reference_type TEXT,
ADD COLUMN IF NOT EXISTS source_reference_id TEXT,
ADD COLUMN IF NOT EXISTS prepaid_financial_credit_eur_cents BIGINT NOT NULL DEFAULT 0,
ADD COLUMN IF NOT EXISTS absorbed_vat_eur_cents BIGINT NOT NULL DEFAULT 0;
CREATE UNIQUE INDEX IF NOT EXISTS idx_billing_invoice_source_reference ON purser.billing_invoices(tenant_id,source_reference_type,source_reference_id) WHERE source_reference_id IS NOT NULL;
CREATE TABLE IF NOT EXISTS purser.prepaid_funding_lots(
 id UUID PRIMARY KEY DEFAULT gen_random_uuid(),tenant_id UUID NOT NULL,
 source_reference_type TEXT NOT NULL,source_reference_id TEXT NOT NULL,
 source_document_id UUID,
 funding_balance_transaction_id UUID,
 classification TEXT NOT NULL CHECK(classification IN ('taxed_advance','legacy_unknown')),
 net_credit_eur_cents BIGINT NOT NULL CHECK(net_credit_eur_cents>=0),
 remaining_net_eur_cents BIGINT NOT NULL CHECK(remaining_net_eur_cents>=0 AND remaining_net_eur_cents<=net_credit_eur_cents),
 vat_eur_cents BIGINT, gross_eur_cents BIGINT,tax_assessment JSONB,
 remaining_vat_eur_cents BIGINT NOT NULL DEFAULT 0,
 revoked_net_eur_cents BIGINT NOT NULL DEFAULT 0,revoked_vat_eur_cents BIGINT NOT NULL DEFAULT 0,
 created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),retention_until TIMESTAMPTZ NOT NULL DEFAULT NOW()+INTERVAL '10 years',
 UNIQUE(tenant_id,source_reference_type,source_reference_id)
);
CREATE INDEX IF NOT EXISTS idx_prepaid_funding_lots_tenant_fifo ON purser.prepaid_funding_lots(tenant_id,created_at,id);
CREATE TABLE IF NOT EXISTS purser.prepaid_funding_allocations(
 tenant_id UUID NOT NULL, lot_id UUID NOT NULL,
 reference_type TEXT NOT NULL,reference_id TEXT NOT NULL,net_eur_cents BIGINT NOT NULL CHECK(net_eur_cents>0),
 vat_eur_cents BIGINT NOT NULL,returned_net_eur_cents BIGINT NOT NULL DEFAULT 0,returned_vat_eur_cents BIGINT NOT NULL DEFAULT 0, created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
 PRIMARY KEY(tenant_id,reference_type,reference_id,lot_id)
);

ALTER TABLE purser.pending_topups DROP CONSTRAINT IF EXISTS pending_topups_tax_document_id_fkey;
ALTER TABLE purser.pending_topups ADD CONSTRAINT pending_topups_tax_document_id_fkey FOREIGN KEY (tax_document_id) REFERENCES purser.billing_invoices(id) NOT VALID;
ALTER TABLE purser.prepaid_funding_lots DROP CONSTRAINT IF EXISTS prepaid_funding_lots_source_document_id_fkey;
ALTER TABLE purser.prepaid_funding_lots ADD CONSTRAINT prepaid_funding_lots_source_document_id_fkey FOREIGN KEY (source_document_id) REFERENCES purser.billing_invoices(id) NOT VALID;
ALTER TABLE purser.prepaid_funding_lots DROP CONSTRAINT IF EXISTS prepaid_funding_lots_funding_balance_transaction_id_fkey;
ALTER TABLE purser.prepaid_funding_lots ADD CONSTRAINT prepaid_funding_lots_funding_balance_transaction_id_fkey FOREIGN KEY (funding_balance_transaction_id) REFERENCES purser.balance_transactions(id) NOT VALID;
ALTER TABLE purser.prepaid_funding_allocations DROP CONSTRAINT IF EXISTS prepaid_funding_allocations_lot_id_fkey;
ALTER TABLE purser.prepaid_funding_allocations ADD CONSTRAINT prepaid_funding_allocations_lot_id_fkey FOREIGN KEY (lot_id) REFERENCES purser.prepaid_funding_lots(id) NOT VALID;

CREATE OR REPLACE FUNCTION purser.allocate_prepaid_funding_inventory()
RETURNS TRIGGER LANGUAGE plpgsql AS $$
DECLARE before_balance BIGINT; remaining BIGINT; gap BIGINT; required BIGINT; take BIGINT; tax_take BIGINT; restore BIGINT; tax_restore BIGINT;
        lot RECORD; allocation RECORD; applied_description TEXT;
BEGIN
 IF NEW.amount_cents=0 THEN RETURN NEW; END IF;
 IF NEW.reference_type IN ('payment_reversal','x402_failed','crypto_reorg','x402_reorg') THEN RETURN NEW; END IF;
 IF NEW.amount_cents>0 THEN
  IF NEW.reference_type IS DISTINCT FROM 'invoice_credit' OR NEW.description NOT LIKE 'Invoice credit returned: %' THEN RETURN NEW; END IF;
  required:=NEW.amount_cents;
  applied_description:=REPLACE(NEW.description,'Invoice credit returned: ','Invoice credit: ');
  FOR allocation IN
   SELECT a.lot_id,a.net_eur_cents,a.returned_net_eur_cents,a.vat_eur_cents,a.returned_vat_eur_cents,a.reference_type,a.reference_id
   FROM purser.prepaid_funding_allocations a JOIN purser.prepaid_funding_lots l ON l.id=a.lot_id AND l.tenant_id=a.tenant_id
   JOIN purser.balance_transactions b ON b.tenant_id=a.tenant_id AND b.id::text=a.reference_id
   WHERE a.tenant_id=NEW.tenant_id AND a.reference_type='balance_transaction' AND b.reference_type='invoice_credit'
   AND b.description=applied_description AND a.returned_net_eur_cents<a.net_eur_cents
   ORDER BY b.created_at DESC,b.id DESC,l.created_at DESC,l.id DESC FOR UPDATE OF a,l
  LOOP
   EXIT WHEN required<=0;
   SELECT id,net_credit_eur_cents,vat_eur_cents,remaining_net_eur_cents,remaining_vat_eur_cents,revoked_net_eur_cents,revoked_vat_eur_cents INTO lot FROM purser.prepaid_funding_lots WHERE tenant_id=NEW.tenant_id AND id=allocation.lot_id FOR UPDATE;
   take:=LEAST(required,allocation.net_eur_cents-allocation.returned_net_eur_cents);
   tax_take:=ROUND(allocation.vat_eur_cents::numeric*(allocation.returned_net_eur_cents+take)/allocation.net_eur_cents)-allocation.returned_vat_eur_cents;
   restore:=LEAST(take,GREATEST(lot.net_credit_eur_cents-lot.revoked_net_eur_cents-lot.remaining_net_eur_cents,0));
   tax_restore:=LEAST(tax_take,GREATEST(COALESCE(lot.vat_eur_cents,0)-lot.revoked_vat_eur_cents-lot.remaining_vat_eur_cents,0));
   UPDATE purser.prepaid_funding_lots SET remaining_net_eur_cents=remaining_net_eur_cents+restore,remaining_vat_eur_cents=remaining_vat_eur_cents+tax_restore WHERE tenant_id=NEW.tenant_id AND id=allocation.lot_id;
   UPDATE purser.prepaid_funding_allocations SET returned_net_eur_cents=returned_net_eur_cents+take,returned_vat_eur_cents=returned_vat_eur_cents+tax_take WHERE tenant_id=NEW.tenant_id AND lot_id=allocation.lot_id AND reference_type=allocation.reference_type AND reference_id=allocation.reference_id;
   required:=required-take;
  END LOOP;
  RETURN NEW;
 END IF;
 before_balance:=GREATEST(NEW.balance_after_cents-NEW.amount_cents,0);
 SELECT COALESCE(SUM(remaining_net_eur_cents),0) INTO remaining FROM purser.prepaid_funding_lots WHERE tenant_id=NEW.tenant_id;
 gap:=before_balance-remaining;
 IF gap>0 THEN
  INSERT INTO purser.prepaid_funding_lots(tenant_id,source_reference_type,source_reference_id,classification,net_credit_eur_cents,remaining_net_eur_cents,tax_assessment)
  VALUES(NEW.tenant_id,'unclassified_balance',NEW.id::text,'legacy_unknown',gap,gap,'{"tax_status":"legacy_unknown"}') ON CONFLICT(tenant_id,source_reference_type,source_reference_id) DO NOTHING;
 END IF;
 required:=LEAST(-NEW.amount_cents,before_balance);
 FOR lot IN SELECT id,net_credit_eur_cents,vat_eur_cents,remaining_net_eur_cents,remaining_vat_eur_cents,revoked_net_eur_cents,revoked_vat_eur_cents FROM purser.prepaid_funding_lots WHERE tenant_id=NEW.tenant_id AND remaining_net_eur_cents>0 ORDER BY created_at,id FOR UPDATE
 LOOP
  EXIT WHEN required<=0;
  take:=LEAST(required,lot.remaining_net_eur_cents);
  tax_take:=CASE WHEN take=lot.remaining_net_eur_cents THEN lot.remaining_vat_eur_cents ELSE ROUND(lot.remaining_vat_eur_cents::numeric*take/lot.remaining_net_eur_cents) END;
  UPDATE purser.prepaid_funding_lots SET remaining_net_eur_cents=remaining_net_eur_cents-take,remaining_vat_eur_cents=remaining_vat_eur_cents-tax_take WHERE tenant_id=NEW.tenant_id AND id=lot.id;
  INSERT INTO purser.prepaid_funding_allocations(tenant_id,lot_id,reference_type,reference_id,net_eur_cents,vat_eur_cents)
  VALUES(NEW.tenant_id,lot.id,'balance_transaction',NEW.id::text,take,tax_take);
  required:=required-take;
 END LOOP;
 RETURN NEW;
END;
$$;
CREATE OR REPLACE TRIGGER prepaid_funding_inventory AFTER INSERT ON purser.balance_transactions FOR EACH ROW EXECUTE FUNCTION purser.allocate_prepaid_funding_inventory();

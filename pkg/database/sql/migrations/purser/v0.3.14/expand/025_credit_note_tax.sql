CREATE OR REPLACE FUNCTION purser.freeze_credit_note_tax()
RETURNS TRIGGER LANGUAGE plpgsql AS $$
DECLARE original_net BIGINT; original_vat BIGINT; original_gross BIGINT;
        original_doc_net BIGINT; original_doc_vat BIGINT; original_doc_gross BIGINT;
        original_currency TEXT; original_policy TEXT; original_summary JSONB;
        original_customer JSONB; original_supplier JSONB;
        previous_gross BIGINT; cumulative_gross BIGINT;
BEGIN
    IF NEW.source_document_type='invoice' THEN
        SELECT net_eur_cents,vat_eur_cents,gross_eur_cents,document_net_minor,document_vat_minor,document_gross_minor,
               COALESCE(presentment_currency,currency),tax_policy_version,tax_summary,customer_snapshot,supplier_snapshot
        INTO original_net,original_vat,original_gross,original_doc_net,original_doc_vat,original_doc_gross,original_currency,original_policy,original_summary,original_customer,original_supplier
        FROM purser.billing_invoices WHERE tenant_id=NEW.tenant_id AND id=NEW.source_document_id FOR UPDATE;
    ELSIF NEW.source_document_type='simplified_invoice' THEN
        SELECT net_eur_cents,vat_eur_cents,amount_eur_cents,net_amount_cents,vat_amount_cents,gross_amount_cents,
               currency,tax_policy_ref,jsonb_build_array(jsonb_build_object('rate_bps',vat_rate_bps,'treatment',tax_validation_status)),tax_assessment->'customer_profile',jsonb_build_object('name',supplier_name,'address',supplier_address,'vat_number',supplier_vat_number,'registration_number',supplier_registration_number)
        INTO original_net,original_vat,original_gross,original_doc_net,original_doc_vat,original_doc_gross,original_currency,original_policy,original_summary,original_customer,original_supplier
        FROM purser.simplified_invoices WHERE tenant_id=NEW.tenant_id AND id=NEW.source_document_id FOR UPDATE;
    ELSIF NEW.source_document_type='crypto_invoice' THEN
        SELECT net_eur_cents,vat_eur_cents,amount_eur_cents,net_amount_cents,vat_amount_cents,gross_amount_cents,
               currency,tax_policy_ref,jsonb_build_array(jsonb_build_object('rate_bps',vat_rate_bps,'treatment',tax_validation_status)),tax_assessment->'customer_profile',jsonb_build_object('name',supplier_name,'address',supplier_address,'vat_number',supplier_vat_number,'registration_number',supplier_registration_number)
        INTO original_net,original_vat,original_gross,original_doc_net,original_doc_vat,original_doc_gross,original_currency,original_policy,original_summary,original_customer,original_supplier
        FROM purser.crypto_invoices WHERE tenant_id=NEW.tenant_id AND id=NEW.source_document_id FOR UPDATE;
    END IF;
    IF NOT FOUND THEN RAISE EXCEPTION 'credit source does not belong to tenant'; END IF;
    IF EXISTS(SELECT 1 FROM purser.credit_notes WHERE tenant_id=NEW.tenant_id AND source_document_type=NEW.source_document_type AND source_document_id=NEW.source_document_id AND reversal_reference_type=NEW.reversal_reference_type AND reversal_reference_id=NEW.reversal_reference_id AND amount_cents=NEW.amount_cents AND currency=NEW.currency) THEN RETURN NEW; END IF;
    IF original_customer IS NOT NULL THEN NEW.customer_snapshot:=original_customer; END IF;
    IF original_supplier IS NOT NULL THEN NEW.supplier_snapshot:=original_supplier; END IF;
    IF original_gross IS NULL OR original_doc_gross IS NULL THEN
        NEW.evidence_json:=NEW.evidence_json||jsonb_build_object('tax_status','legacy_unknown');
        RETURN NEW;
    END IF;
    IF NEW.currency<>original_currency THEN RAISE EXCEPTION 'credit currency differs from source'; END IF;
    SELECT COALESCE(SUM(amount_cents),0) INTO previous_gross FROM purser.credit_notes
    WHERE tenant_id=NEW.tenant_id AND source_document_type=NEW.source_document_type AND source_document_id=NEW.source_document_id;
    cumulative_gross:=previous_gross+NEW.amount_cents;
    IF cumulative_gross>original_doc_gross OR original_doc_gross<=0 THEN RAISE EXCEPTION 'cumulative credits exceed original supply'; END IF;
    NEW.document_gross_minor:=NEW.amount_cents;
    NEW.document_vat_minor:=ROUND(original_doc_vat::numeric*cumulative_gross/original_doc_gross)-ROUND(original_doc_vat::numeric*previous_gross/original_doc_gross);
    NEW.document_net_minor:=NEW.document_gross_minor-NEW.document_vat_minor;
    NEW.gross_eur_cents:=ROUND(original_gross::numeric*cumulative_gross/original_doc_gross)-ROUND(original_gross::numeric*previous_gross/original_doc_gross);
    NEW.vat_eur_cents:=ROUND(original_vat::numeric*cumulative_gross/original_doc_gross)-ROUND(original_vat::numeric*previous_gross/original_doc_gross);
    NEW.net_eur_cents:=NEW.gross_eur_cents-NEW.vat_eur_cents;
    NEW.tax_policy_version:=original_policy;
    NEW.tax_summary:=jsonb_build_object('source_tax_groups',original_summary,'net_eur_cents',NEW.net_eur_cents,'vat_eur_cents',NEW.vat_eur_cents,'gross_eur_cents',NEW.gross_eur_cents);
    RETURN NEW;
END;
$$;
CREATE OR REPLACE TRIGGER credit_note_tax_snapshot BEFORE INSERT ON purser.credit_notes FOR EACH ROW EXECUTE FUNCTION purser.freeze_credit_note_tax();

CREATE OR REPLACE FUNCTION purser.retain_issued_invoice_line()
RETURNS TRIGGER LANGUAGE plpgsql AS $$
DECLARE issued BOOLEAN;
BEGIN
 IF TG_OP='INSERT' THEN
 SELECT tax_status IN ('ready','no_supply') INTO issued FROM purser.billing_invoices WHERE tenant_id=NEW.tenant_id AND id=NEW.invoice_id;
 ELSE
 SELECT tax_status IN ('ready','no_supply') INTO issued FROM purser.billing_invoices WHERE tenant_id=OLD.tenant_id AND id=OLD.invoice_id;
 END IF;
 IF issued THEN
  IF TG_OP IN ('DELETE','INSERT') THEN RAISE EXCEPTION 'issued invoice lines are immutable'; END IF;
  IF ROW(NEW.tenant_id,NEW.invoice_id,NEW.line_key,NEW.amount,NEW.quantity,NEW.unit_price,NEW.description,NEW.currency,NEW.net_eur_cents,NEW.vat_eur_cents,NEW.gross_eur_cents,NEW.vat_rate_bps,NEW.tax_treatment,NEW.tax_point_at)
  IS DISTINCT FROM ROW(OLD.tenant_id,OLD.invoice_id,OLD.line_key,OLD.amount,OLD.quantity,OLD.unit_price,OLD.description,OLD.currency,OLD.net_eur_cents,OLD.vat_eur_cents,OLD.gross_eur_cents,OLD.vat_rate_bps,OLD.tax_treatment,OLD.tax_point_at)
  THEN RAISE EXCEPTION 'issued invoice lines are immutable'; END IF;
 END IF;
 IF TG_OP='DELETE' THEN RETURN OLD; END IF;
 RETURN NEW;
END;
$$;
CREATE OR REPLACE TRIGGER invoice_line_tax_guard BEFORE INSERT OR UPDATE OR DELETE ON purser.invoice_line_items FOR EACH ROW EXECUTE FUNCTION purser.retain_issued_invoice_line();


CREATE OR REPLACE FUNCTION purser.retain_issued_credit_note()
RETURNS TRIGGER LANGUAGE plpgsql AS $$
BEGIN
 IF ROW(NEW.tenant_id,NEW.credit_note_number,NEW.source_document_type,NEW.source_document_id,NEW.reversal_reference_type,NEW.reversal_reference_id,NEW.amount_cents,NEW.currency,NEW.reason,NEW.issued_at,NEW.customer_snapshot,NEW.supplier_snapshot,NEW.net_eur_cents,NEW.vat_eur_cents,NEW.gross_eur_cents,NEW.document_net_minor,NEW.document_vat_minor,NEW.document_gross_minor,NEW.tax_policy_version,NEW.tax_summary)
 IS DISTINCT FROM ROW(OLD.tenant_id,OLD.credit_note_number,OLD.source_document_type,OLD.source_document_id,OLD.reversal_reference_type,OLD.reversal_reference_id,OLD.amount_cents,OLD.currency,OLD.reason,OLD.issued_at,OLD.customer_snapshot,OLD.supplier_snapshot,OLD.net_eur_cents,OLD.vat_eur_cents,OLD.gross_eur_cents,OLD.document_net_minor,OLD.document_vat_minor,OLD.document_gross_minor,OLD.tax_policy_version,OLD.tax_summary)
 THEN RAISE EXCEPTION 'issued credit note facts are immutable'; END IF;
 RETURN NEW;
END;
$$;
CREATE OR REPLACE TRIGGER issued_credit_note_guard BEFORE UPDATE ON purser.credit_notes FOR EACH ROW EXECUTE FUNCTION purser.retain_issued_credit_note();

CREATE OR REPLACE FUNCTION purser.next_billing_document_number(prefix TEXT)
RETURNS TEXT LANGUAGE plpgsql VOLATILE AS $$
DECLARE sequence_name TEXT;
BEGIN
 IF prefix NOT IN ('INV','STM','CN') THEN RAISE EXCEPTION 'unsupported billing document prefix'; END IF;
 IF current_setting('frameworks.billing_preview',true)='true' THEN
  RETURN 'PREVIEW-'||prefix||'-'||gen_random_uuid()::text;
 END IF;
 sequence_name:=CASE WHEN prefix='CN' THEN 'purser.credit_note_number_seq' ELSE 'purser.billing_invoice_number_seq' END;
 RETURN prefix||'-'||lpad(nextval(sequence_name::regclass)::text,10,'0');
END;
$$;
ALTER TABLE purser.billing_invoices ALTER COLUMN invoice_number SET DEFAULT purser.next_billing_document_number('INV');

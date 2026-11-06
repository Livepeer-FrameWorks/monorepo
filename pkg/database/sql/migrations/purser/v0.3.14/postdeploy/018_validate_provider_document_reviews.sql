ALTER TABLE purser.provider_billing_documents VALIDATE CONSTRAINT provider_document_review_shape;
ALTER TABLE purser.provider_billing_documents VALIDATE CONSTRAINT provider_document_review_math;
DO $$
BEGIN
 IF NOT EXISTS (
  SELECT 1 FROM pg_index
  WHERE indexrelid=to_regclass('purser.idx_provider_document_tax_review_due')
   AND indrelid='purser.provider_billing_documents'::regclass AND indisvalid AND indisready
 ) THEN RAISE EXCEPTION 'provider document tax review index is absent or invalid'; END IF;
END;
$$;

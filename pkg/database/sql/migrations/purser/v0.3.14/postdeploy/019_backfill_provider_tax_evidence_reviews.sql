DO $$
DECLARE review_tenant UUID;
BEGIN
 FOR review_tenant IN
  SELECT DISTINCT tenant_id FROM purser.provider_billing_documents
  WHERE provider='stripe' AND tax_status='ready'
   AND (tax_assessment @> '{"review_required":true}'::jsonb
    OR tax_assessment @> '{"evidence":{"review_required":true}}'::jsonb
    OR tax_assessment->>'vat_validation_mode'='outage_grace'
    OR tax_assessment#>>'{evidence,vat_validation_mode}'='outage_grace')
 LOOP
  INSERT INTO purser.crypto_accounting_anomalies(
   tenant_id,kind,network,reference_type,reference_id,amount_cents,currency,detail,evidence_json
  )
  SELECT document.tenant_id,'provider_tax_evidence_review','stripe',document.document_kind,
   document.id::text,0,'EUR',
   'Issued Stripe document used provisional tax evidence; review the retained claim',
   document.tax_assessment
  FROM purser.provider_billing_documents document
  WHERE document.tenant_id=review_tenant AND document.provider='stripe' AND document.tax_status='ready'
   AND (document.tax_assessment @> '{"review_required":true}'::jsonb
    OR document.tax_assessment @> '{"evidence":{"review_required":true}}'::jsonb
    OR document.tax_assessment->>'vat_validation_mode'='outage_grace'
    OR document.tax_assessment#>>'{evidence,vat_validation_mode}'='outage_grace')
  ON CONFLICT(kind,reference_type,reference_id) DO NOTHING;
 END LOOP;
END;
$$;

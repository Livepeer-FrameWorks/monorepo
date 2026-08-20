ALTER TABLE purser.credit_notes
    VALIDATE CONSTRAINT credit_notes_source_document_type_check;

ALTER TABLE purser.crypto_wallets
    VALIDATE CONSTRAINT crypto_wallets_tax_document_kind_check;

ALTER TABLE purser.x402_payment_quotes
    VALIDATE CONSTRAINT x402_payment_quotes_tax_document_kind_check;

ALTER TABLE purser.simplified_invoices
    VALIDATE CONSTRAINT chk_simplified_invoice_service_quantity;

ALTER TABLE purser.simplified_invoices
    VALIDATE CONSTRAINT chk_simplified_invoice_evidence_status;

ALTER TABLE purser.crypto_invoices
    VALIDATE CONSTRAINT chk_crypto_invoice_evidence_status;

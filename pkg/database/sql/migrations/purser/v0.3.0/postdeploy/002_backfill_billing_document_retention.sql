-- Backfill additive document identifiers and retention deadlines after every
-- writer has deployed defaults from expand/014.

WITH numbered AS (
    SELECT id, nextval('purser.billing_invoice_number_seq') AS sequence_number
    FROM purser.billing_invoices
    WHERE invoice_number IS NULL
    ORDER BY created_at, id
)
UPDATE purser.billing_invoices invoice
SET invoice_number = 'INV-' || LPAD(numbered.sequence_number::text, 10, '0')
FROM numbered
WHERE invoice.id = numbered.id;

UPDATE purser.billing_invoices
SET retention_until = COALESCE(created_at, NOW()) + INTERVAL '10 years'
WHERE retention_until IS NULL;

UPDATE purser.billing_payments
SET retention_until = COALESCE(created_at, NOW()) + INTERVAL '10 years'
WHERE retention_until IS NULL;

UPDATE purser.simplified_invoices
SET retention_until = COALESCE(issued_at, created_at, NOW()) + INTERVAL '10 years'
WHERE retention_until IS NULL;

UPDATE purser.credit_notes
SET retention_until = COALESCE(issued_at, NOW()) + INTERVAL '10 years'
WHERE retention_until IS NULL;

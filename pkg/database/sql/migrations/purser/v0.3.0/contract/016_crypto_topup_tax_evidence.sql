-- New binaries record an explicit tax-policy version. Remove the superseded
-- deployment-signoff field only after the expand-compatible rollout.

ALTER TABLE purser.simplified_invoices
    DROP COLUMN IF EXISTS accounting_signoff_ref;

-- A move to Free ends collection without closing the account.
ALTER TABLE purser.billing_collection_writeoffs
    DROP CONSTRAINT IF EXISTS chk_billing_collection_writeoff_reason;
ALTER TABLE purser.billing_collection_writeoffs
    ADD CONSTRAINT chk_billing_collection_writeoff_reason
    CHECK (reason IN ('account_closed', 'moved_to_free', 'operator_approved')) NOT VALID;

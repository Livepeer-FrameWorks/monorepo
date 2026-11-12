DO $$ BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM pg_index WHERE indexrelid = to_regclass('commodore.idx_auth_session_devices_retention') AND indisvalid AND indisready
    ) THEN RAISE EXCEPTION 'commodore.idx_auth_session_devices_retention is missing or invalid'; END IF;
END $$;
DO $$ BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM pg_index WHERE indexrelid = to_regclass('commodore.idx_account_email_outbox_due') AND indisvalid AND indisready
    ) THEN RAISE EXCEPTION 'commodore.idx_account_email_outbox_due is missing or invalid'; END IF;
END $$;
DO $$ BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM pg_index WHERE indexrelid = to_regclass('commodore.idx_account_email_outbox_terminal') AND indisvalid AND indisready
    ) THEN RAISE EXCEPTION 'commodore.idx_account_email_outbox_terminal is missing or invalid'; END IF;
END $$;
ALTER TABLE commodore.account_email_outbox VALIDATE CONSTRAINT chk_commodore_account_email_outbox_purpose;
ALTER TABLE commodore.account_email_outbox VALIDATE CONSTRAINT chk_commodore_account_email_outbox_status;
ALTER TABLE commodore.account_email_outbox VALIDATE CONSTRAINT chk_commodore_account_email_outbox_security;

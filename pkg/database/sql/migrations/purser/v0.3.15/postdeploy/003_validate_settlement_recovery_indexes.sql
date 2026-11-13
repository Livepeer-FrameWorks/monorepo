DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM pg_index WHERE indexrelid = to_regclass('purser.idx_provider_inbox_blocked') AND indisvalid AND indisready
    ) THEN
        RAISE EXCEPTION 'purser index idx_provider_inbox_blocked is missing or not ready';
    END IF;
    IF NOT EXISTS (
        SELECT 1 FROM pg_index WHERE indexrelid = to_regclass('purser.idx_provider_inbox_bound_recovery_tenant') AND indisvalid AND indisready
    ) THEN
        RAISE EXCEPTION 'purser index idx_provider_inbox_bound_recovery_tenant is missing or not ready';
    END IF;
    IF NOT EXISTS (
        SELECT 1 FROM pg_index WHERE indexrelid = to_regclass('purser.idx_provider_settlements_valuation_queue') AND indisvalid AND indisready
    ) THEN
        RAISE EXCEPTION 'purser index idx_provider_settlements_valuation_queue is missing or not ready';
    END IF;
    IF NOT EXISTS (
        SELECT 1 FROM pg_index WHERE indexrelid = to_regclass('purser.idx_provider_settlements_supported_queue') AND indisvalid AND indisready
    ) THEN
        RAISE EXCEPTION 'purser index idx_provider_settlements_supported_queue is missing or not ready';
    END IF;
    IF NOT EXISTS (
        SELECT 1 FROM pg_index WHERE indexrelid = to_regclass('purser.idx_provider_inbox_blocked_payment') AND indisvalid AND indisready
    ) THEN
        RAISE EXCEPTION 'purser index idx_provider_inbox_blocked_payment is missing or not ready';
    END IF;
    IF NOT EXISTS (
        SELECT 1 FROM pg_index WHERE indexrelid = to_regclass('purser.idx_provider_settlements_recovery_listing') AND indisvalid AND indisready
    ) THEN
        RAISE EXCEPTION 'purser index idx_provider_settlements_recovery_listing is missing or not ready';
    END IF;
    IF NOT EXISTS (
        SELECT 1 FROM pg_index WHERE indexrelid = to_regclass('purser.idx_provider_settlements_recovery_tenant') AND indisvalid AND indisready
    ) THEN
        RAISE EXCEPTION 'purser index idx_provider_settlements_recovery_tenant is missing or not ready';
    END IF;
    IF NOT EXISTS (
        SELECT 1 FROM pg_index WHERE indexrelid = to_regclass('purser.idx_provider_settlements_recovery_state') AND indisvalid AND indisready
    ) THEN
        RAISE EXCEPTION 'purser index idx_provider_settlements_recovery_state is missing or not ready';
    END IF;
    IF NOT EXISTS (
        SELECT 1 FROM pg_index WHERE indexrelid = to_regclass('purser.idx_provider_settlements_identity_backfill') AND indisvalid AND indisready
    ) THEN
        RAISE EXCEPTION 'purser index idx_provider_settlements_identity_backfill is missing or not ready';
    END IF;
END $$;

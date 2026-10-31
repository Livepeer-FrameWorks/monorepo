DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM pg_index state
        WHERE state.indexrelid = to_regclass('purser.idx_operator_credit_clawback_origin')
          AND state.indrelid = 'purser.operator_credit_ledger'::regclass
          AND state.indisvalid AND state.indisready
          AND pg_get_indexdef(state.indexrelid) !~ ' HASH([, )])'
    ) THEN
        RAISE EXCEPTION 'idx_operator_credit_clawback_origin is absent, invalid, or hash-sharded';
    END IF;
    IF NOT EXISTS (
        SELECT 1 FROM pg_index state
        WHERE state.indexrelid = to_regclass('purser.idx_operator_grants_failed')
          AND state.indrelid = 'purser.subscription_operator_grants'::regclass
          AND state.indisvalid AND state.indisready
          AND pg_get_indexdef(state.indexrelid) !~ ' HASH([, )])'
    ) THEN
        RAISE EXCEPTION 'idx_operator_grants_failed is absent, invalid, or hash-sharded';
    END IF;
    IF NOT EXISTS (
        SELECT 1 FROM pg_index state
        WHERE state.indexrelid = to_regclass('purser.idx_subscription_fallback_due_retry')
          AND state.indrelid = 'purser.tenant_subscriptions'::regclass
          AND state.indisvalid AND state.indisready
          AND pg_get_indexdef(state.indexrelid) !~ ' HASH([, )])'
    ) THEN
        RAISE EXCEPTION 'idx_subscription_fallback_due_retry is absent, invalid, or hash-sharded';
    END IF;
    IF NOT EXISTS (
        SELECT 1 FROM pg_index state
        WHERE state.indexrelid = to_regclass('purser.idx_subscription_fallback_notice_retry')
          AND state.indrelid = 'purser.tenant_subscriptions'::regclass
          AND state.indisvalid AND state.indisready
          AND pg_get_indexdef(state.indexrelid) !~ ' HASH([, )])'
    ) THEN
        RAISE EXCEPTION 'idx_subscription_fallback_notice_retry is absent, invalid, or hash-sharded';
    END IF;
    IF NOT EXISTS (
        SELECT 1 FROM pg_index state
        WHERE state.indexrelid = to_regclass('purser.idx_subscription_fallback_failed')
          AND state.indrelid = 'purser.tenant_subscriptions'::regclass
          AND state.indisvalid AND state.indisready
          AND pg_get_indexdef(state.indexrelid) !~ ' HASH([, )])'
    ) THEN
        RAISE EXCEPTION 'idx_subscription_fallback_failed is absent, invalid, or hash-sharded';
    END IF;
    IF NOT EXISTS (
        SELECT 1 FROM pg_index state
        WHERE state.indexrelid = to_regclass('purser.idx_subscription_fallback_wait')
          AND state.indrelid = 'purser.tenant_subscriptions'::regclass
          AND state.indisvalid AND state.indisready
          AND pg_get_indexdef(state.indexrelid) !~ ' HASH([, )])'
    ) THEN
        RAISE EXCEPTION 'idx_subscription_fallback_wait is absent, invalid, or hash-sharded';
    END IF;
    IF NOT EXISTS (
        SELECT 1 FROM pg_index state
        WHERE state.indexrelid = to_regclass('purser.idx_subscription_fallback_notice_failed')
          AND state.indrelid = 'purser.tenant_subscriptions'::regclass
          AND state.indisvalid AND state.indisready
          AND pg_get_indexdef(state.indexrelid) !~ ' HASH([, )])'
          AND pg_get_indexdef(state.indexrelid) LIKE '% INCLUDE (id)%'
    ) THEN
        RAISE EXCEPTION 'idx_subscription_fallback_notice_failed is absent, invalid, hash-sharded, or missing its covering id';
    END IF;
    IF NOT EXISTS (
        SELECT 1 FROM pg_index state
        WHERE state.indexrelid = to_regclass('purser.idx_provider_inbox_failed_attempts')
          AND state.indrelid = 'purser.provider_webhook_inbox'::regclass
          AND state.indisvalid AND state.indisready
          AND pg_get_indexdef(state.indexrelid) !~ ' HASH([, )])'
    ) THEN
        RAISE EXCEPTION 'idx_provider_inbox_failed_attempts is absent, invalid, or hash-sharded';
    END IF;
    IF NOT EXISTS (
        SELECT 1 FROM pg_index state
        WHERE state.indexrelid = to_regclass('purser.idx_provider_inbox_unresolved_age')
          AND state.indrelid = 'purser.provider_webhook_inbox'::regclass
          AND state.indisvalid AND state.indisready
          AND pg_get_indexdef(state.indexrelid) !~ ' HASH([, )])'
    ) THEN
        RAISE EXCEPTION 'idx_provider_inbox_unresolved_age is absent, invalid, or hash-sharded';
    END IF;
    IF NOT EXISTS (
        SELECT 1 FROM pg_index state
        WHERE state.indexrelid = to_regclass('purser.idx_provider_inbox_recovery_listing')
          AND state.indrelid = 'purser.provider_webhook_inbox'::regclass
          AND state.indisvalid AND state.indisready
          AND pg_get_indexdef(state.indexrelid) !~ ' HASH([, )])'
    ) THEN
        RAISE EXCEPTION 'idx_provider_inbox_recovery_listing is absent, invalid, or hash-sharded';
    END IF;
    IF NOT EXISTS (
        SELECT 1 FROM pg_index state
        WHERE state.indexrelid = to_regclass('purser.idx_provider_inbox_recovery_tenant')
          AND state.indrelid = 'purser.provider_webhook_inbox'::regclass
          AND state.indisvalid AND state.indisready
          AND pg_get_indexdef(state.indexrelid) !~ ' HASH([, )])'
    ) THEN
        RAISE EXCEPTION 'idx_provider_inbox_recovery_tenant is absent, invalid, or hash-sharded';
    END IF;
    IF NOT EXISTS (
        SELECT 1 FROM pg_index state
        WHERE state.indexrelid = to_regclass('purser.idx_subscription_stripe_customer_latest')
          AND state.indrelid = 'purser.tenant_subscriptions'::regclass
          AND state.indisvalid AND state.indisready
          AND pg_get_indexdef(state.indexrelid) !~ ' HASH([, )])'
    ) THEN
        RAISE EXCEPTION 'idx_subscription_stripe_customer_latest is absent, invalid, or hash-sharded';
    END IF;
END;
$$;

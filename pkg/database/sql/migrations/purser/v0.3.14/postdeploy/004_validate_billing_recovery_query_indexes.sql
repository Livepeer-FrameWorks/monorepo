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
          AND pg_get_indexdef(state.indexrelid, 1, true) IN (
              'free_fallback_notice_attempted_at', 'free_fallback_notice_attempted_at NULLS FIRST',
              'free_fallback_notice_attempted_at ASC NULLS FIRST')
          AND pg_get_indexdef(state.indexrelid) ~ '[(]free_fallback_notice_attempted_at( ASC)? NULLS FIRST,'
          AND pg_get_indexdef(state.indexrelid, 2, true) IN ('free_fallback_at', 'free_fallback_at ASC')
          AND pg_get_indexdef(state.indexrelid, 3, true) IN ('tenant_id', 'tenant_id ASC')
          AND pg_get_indexdef(state.indexrelid) LIKE '% INCLUDE (id, free_fallback_notice_failures)%'
          AND regexp_replace(pg_get_expr(state.indpred, state.indrelid), '[[:space:]]', '', 'g') =
              '((free_fallback_atISNOTNULL)AND((free_fallback_moved_atISNOTNULL)OR(free_fallback_notified_atISNULL))AND(free_fallback_notice_abandoned_atISNULL))'
    ) THEN
        RAISE EXCEPTION 'idx_subscription_fallback_notice_retry is absent, invalid, hash-sharded, or has a different retry key/predicate';
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
          AND pg_get_indexdef(state.indexrelid, 1, true) IN ('free_fallback_notice_failures', 'free_fallback_notice_failures ASC')
          AND pg_get_indexdef(state.indexrelid, 2, true) IN ('tenant_id', 'tenant_id ASC')
          AND regexp_replace(pg_get_expr(state.indpred, state.indrelid), '[[:space:]()]', '', 'g') =
              'free_fallback_atISNOTNULLANDfree_fallback_notice_errorISNOTNULLANDfree_fallback_notice_abandoned_atISNULL'
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
    IF NOT EXISTS (
        SELECT 1 FROM pg_index state
        WHERE state.indexrelid = to_regclass('purser.idx_mollie_observations_attached_subscription')
          AND state.indrelid = 'purser.mollie_payment_observations'::regclass
          AND state.indisvalid AND state.indisready
          AND pg_get_indexdef(state.indexrelid) !~ ' HASH([, )])'
          AND pg_get_indexdef(state.indexrelid, 1, true) IN ('tenant_id', 'tenant_id ASC')
          AND pg_get_indexdef(state.indexrelid, 2, true) IN ('mollie_subscription_id', 'mollie_subscription_id ASC')
          AND pg_get_indexdef(state.indexrelid) LIKE '% INCLUDE (invoice_id, resolved_at)%'
          AND pg_get_expr(state.indpred, state.indrelid) LIKE '%resolution%attached%resolved_at IS NOT NULL%'
    ) THEN
        RAISE EXCEPTION 'idx_mollie_observations_attached_subscription is absent, invalid, or has different funding keys/covering columns';
    END IF;
END;
$$;

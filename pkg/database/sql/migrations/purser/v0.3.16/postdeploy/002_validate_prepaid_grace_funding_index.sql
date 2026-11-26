DO $$
BEGIN
 IF NOT EXISTS (
  SELECT 1 FROM pg_index state
  WHERE state.indexrelid=to_regclass('purser.idx_prepaid_grace_settled_topup')
    AND state.indrelid='purser.balance_transactions'::regclass
    AND state.indisvalid AND state.indisready
 ) THEN RAISE EXCEPTION 'prepaid grace funding index is absent, invalid, not ready, or belongs to another table'; END IF;
END;
$$;

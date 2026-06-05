-- Validate the regex CHECK constraints added NOT VALID in
-- v0.2.82/expand/002 (now that all running binaries write canonical
-- meter names) and widen the usage_type columns so the regex's max
-- length (64) is actually reachable in storage.
--
-- VALIDATE CONSTRAINT scans existing rows under a SHARE UPDATE EXCLUSIVE
-- lock; non-blocking for SELECT/INSERT/UPDATE/DELETE.
--
-- The varchar(50)→varchar(64) widening is a metadata-only change in
-- modern Postgres (no table rewrite), but the validator categorically
-- rejects ALTER COLUMN TYPE in expand so the widen lives here.

ALTER TABLE purser.tier_pricing_rules
    VALIDATE CONSTRAINT chk_tier_pricing_meter;

ALTER TABLE purser.subscription_pricing_overrides
    VALIDATE CONSTRAINT chk_subscription_pricing_meter;

ALTER TABLE purser.usage_records
    ALTER COLUMN usage_type TYPE VARCHAR(64);

ALTER TABLE purser.usage_records_quarantine
    ALTER COLUMN usage_type TYPE VARCHAR(64);

ALTER TABLE purser.operator_credit_ledger
    VALIDATE CONSTRAINT chk_op_credit_source;

ALTER TABLE purser.usage_adjustments
    VALIDATE CONSTRAINT chk_usage_adjustments_status;

ALTER TABLE purser.usage_adjustments
    VALIDATE CONSTRAINT chk_usage_adjustments_value_kind;

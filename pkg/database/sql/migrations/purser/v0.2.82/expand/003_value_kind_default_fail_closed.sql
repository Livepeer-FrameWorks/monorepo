-- Change the value_kind default on purser.usage_records and
-- purser.usage_records_quarantine from 'delta' to 'ignored'.
-- Writers that bypass validateUsageRecord (or any INSERT that omits
-- value_kind) must NOT land as billable. Billing aggregation filters
-- value_kind = 'delta' AND granularity = 'minute_5'; flipping the
-- default to 'ignored' makes the safe path the default.
--
-- Metadata-only change; existing rows retain their stored value_kind.

ALTER TABLE purser.usage_records
    ALTER COLUMN value_kind SET DEFAULT 'ignored';

ALTER TABLE purser.usage_records
    ALTER COLUMN granularity SET DEFAULT 'minute_5';

ALTER TABLE purser.usage_records_quarantine
    ALTER COLUMN value_kind SET DEFAULT 'ignored';

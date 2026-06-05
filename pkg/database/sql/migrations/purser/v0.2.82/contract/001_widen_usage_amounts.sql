ALTER TABLE purser.usage_records
    ALTER COLUMN usage_value TYPE DECIMAL(20,6);

ALTER TABLE purser.usage_records_quarantine
    ALTER COLUMN usage_value TYPE DECIMAL(20,6);

ALTER TABLE purser.usage_adjustments
    ALTER COLUMN delta_value TYPE DECIMAL(20,6);

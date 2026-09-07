-- Existing v0.3.0 installations may have recorded migration 001 before
-- delivery_kind and platform became supported dimensions. Re-seed those rows
-- without changing the checksum of the already-applied migration.
INSERT INTO purser.meter_definitions
    (meter, unit, aggregation, display_name, allowed_dimensions, default_priceable)
VALUES
    ('delivered_minutes', 'minute', 'sum', 'Delivered minutes', '{delivery_kind,platform}', TRUE),
    ('egress_gb', 'gibibyte', 'sum', 'Egress bandwidth', '{delivery_kind,platform}', TRUE)
ON CONFLICT (meter) DO UPDATE SET
    allowed_dimensions = EXCLUDED.allowed_dimensions;

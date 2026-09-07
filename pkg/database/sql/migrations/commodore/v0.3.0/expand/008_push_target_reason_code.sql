-- Preserve the bounded machine reason independently from the sanitized
-- operator-facing error text. Existing rows have no historical reason signal.
ALTER TABLE commodore.push_targets
    ADD COLUMN IF NOT EXISTS reason_code VARCHAR(50) NOT NULL DEFAULT 'unspecified';

ALTER TABLE commodore.push_targets
    DROP CONSTRAINT IF EXISTS ck_commodore_push_targets_reason_code;
ALTER TABLE commodore.push_targets
    ADD CONSTRAINT ck_commodore_push_targets_reason_code
    CHECK (reason_code IN (
        'unspecified', 'connected', 'completed', 'destination_rejected',
        'network_error', 'process_error', 'capacity_exhausted',
        'configuration_error', 'edge_upgrade_required', 'stopped'
    )) NOT VALID;

-- Active recipients inherit version validity; correction recipients have an
-- immutable ceiling matching their signed envelope, even after a later regrant.
ALTER TABLE commodore.media_authority_deliveries
    ADD COLUMN IF NOT EXISTS correction_until TIMESTAMPTZ;

CREATE OR REPLACE VIEW commodore.media_authority_actionable_rejections AS
SELECT delivery.authority_kind, delivery.authority_id, delivery.authority_version, delivery.cell_id
FROM commodore.media_authority_deliveries AS delivery
JOIN commodore.media_authority_current AS current
  ON current.authority_kind = delivery.authority_kind
 AND current.authority_id = delivery.authority_id
 AND current.authority_version = delivery.authority_version
LEFT JOIN commodore.media_authority_targets AS target
  ON target.authority_kind = delivery.authority_kind
 AND target.authority_id = delivery.authority_id
 AND target.cell_id = delivery.cell_id
LEFT JOIN commodore.media_authority_distribution AS distribution
  ON distribution.authority_kind = delivery.authority_kind
 AND distribution.authority_id = delivery.authority_id
 AND distribution.cell_id = delivery.cell_id
LEFT JOIN commodore.media_authority_cell_ack_resets AS reset
  ON reset.cell_id = delivery.cell_id
WHERE delivery.status = 'rejected'
  AND EXISTS (
      SELECT 1 FROM commodore.media_authority_versions AS versions
      JOIN commodore.media_authority_deliveries AS copy USING (authority_kind, authority_id, authority_version)
      WHERE versions.authority_kind = delivery.authority_kind
        AND versions.authority_id = delivery.authority_id
        AND copy.cell_id = delivery.cell_id
        AND LEAST(versions.valid_until, copy.correction_until) > NOW()
        AND (versions.authority_version = current.authority_version OR (
            versions.authority_version <= target.highest_targeted_version
            AND versions.authority_version >= CASE
                WHEN distribution.last_acknowledged_at > COALESCE(reset.reset_at, '-infinity'::timestamptz)
                THEN distribution.highest_acknowledged_version ELSE 0 END))
  );

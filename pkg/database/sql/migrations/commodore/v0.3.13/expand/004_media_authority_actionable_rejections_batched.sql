-- A rejected current delivery needs intervention only while it, or an older
-- copy this cell could still hold, can authorize a decision.
--
-- Every rejected delivery is first resolved to the versions its cell could
-- still hold, with one batched key lookup per table for the whole set, so only
-- the search for a copy is made per rejected current delivery. Looking each
-- table up per delivery costs YugabyteDB a storage round trip per table and row.
CREATE OR REPLACE VIEW commodore.media_authority_actionable_rejections AS
WITH rejected AS MATERIALIZED (
    SELECT delivery.authority_kind, delivery.authority_id, delivery.authority_version, delivery.cell_id,
           target.highest_targeted_version,
           CASE
               WHEN distribution.last_acknowledged_at > COALESCE(reset.reset_at, '-infinity'::timestamptz)
               THEN distribution.highest_acknowledged_version ELSE 0
           END AS lowest_held_version
    FROM commodore.media_authority_deliveries AS delivery
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
)
SELECT rejected.authority_kind, rejected.authority_id, rejected.authority_version, rejected.cell_id
FROM rejected
JOIN commodore.media_authority_current AS current
  ON current.authority_kind = rejected.authority_kind
 AND current.authority_id = rejected.authority_id
 AND current.authority_version = rejected.authority_version
WHERE EXISTS (
    SELECT 1 FROM commodore.media_authority_versions AS versions
    JOIN commodore.media_authority_deliveries AS copy USING (authority_kind, authority_id, authority_version)
    WHERE versions.authority_kind = rejected.authority_kind
      AND versions.authority_id = rejected.authority_id
      AND copy.cell_id = rejected.cell_id
      AND LEAST(versions.valid_until, copy.correction_until) > NOW()
      AND (versions.authority_version = rejected.authority_version OR (
          versions.authority_version <= rejected.highest_targeted_version
          AND versions.authority_version >= rejected.lowest_held_version))
);

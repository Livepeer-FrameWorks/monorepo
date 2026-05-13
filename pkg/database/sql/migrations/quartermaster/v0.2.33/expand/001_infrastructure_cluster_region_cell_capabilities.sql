-- First-class region/cell/class metadata on the cluster row. Used by the
-- resolver for plan-tier + GeoIP scoring, and by the event envelope to stamp
-- source_region/source_cluster_id without joining nodes. cluster_class drives
-- the plan-tier admission filter (free → official only; premium → official +
-- marketplace; enterprise → all three).
--
-- Column-add only. Values are populated by cluster_provision from the gitops
-- manifest on the next apply — the manifest is the source of truth for
-- region/cell/class, not a derivation from existing rows.
-- Schema source of truth: pkg/database/sql/schema/quartermaster.sql

ALTER TABLE quartermaster.infrastructure_clusters
    ADD COLUMN IF NOT EXISTS region_id VARCHAR(50),
    ADD COLUMN IF NOT EXISTS cell_id VARCHAR(100),
    ADD COLUMN IF NOT EXISTS cluster_class VARCHAR(50);

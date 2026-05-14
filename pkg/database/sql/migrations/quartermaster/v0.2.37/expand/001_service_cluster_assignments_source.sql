-- Provenance column on service_cluster_assignments. Mirrors
-- infrastructure_nodes.enrollment_origin so GitOps-seeded media-cell
-- assignments and operator-attached runtime overlays have separate ownership.
-- Runtime upserts must preserve the existing source on conflict; only explicit
-- adopt/unmanage operations flip provenance between gitops_seed /
-- adopted_local / runtime.
--
-- Column-add only. Default 'runtime' is correct for the backfill because
-- every existing row was written by a runtime RPC (AssignServiceToCluster
-- or EnableSelfHosting).
-- Schema source of truth: pkg/database/sql/schema/quartermaster.sql

ALTER TABLE quartermaster.service_cluster_assignments
    ADD COLUMN IF NOT EXISTS source VARCHAR(32) NOT NULL DEFAULT 'runtime';

DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1
        FROM information_schema.table_constraints
        WHERE table_schema = 'quartermaster'
          AND table_name = 'service_cluster_assignments'
          AND constraint_name = 'service_cluster_assignments_source_check'
    ) THEN
        ALTER TABLE quartermaster.service_cluster_assignments
            ADD CONSTRAINT service_cluster_assignments_source_check
            CHECK (source IN ('gitops_seed', 'runtime', 'adopted_local')) NOT VALID;
    END IF;
END $$;

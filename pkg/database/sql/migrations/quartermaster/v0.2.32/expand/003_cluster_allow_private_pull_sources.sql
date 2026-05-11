-- Cluster-level pull-source private-network allowance. Default FALSE so
-- platform-official clusters reject tenant-private upstreams; self-hosted
-- clusters explicitly opt in via cluster.yaml + bootstrap reconcile.
-- Schema source of truth: pkg/database/sql/schema/quartermaster.sql

ALTER TABLE quartermaster.infrastructure_clusters
    ADD COLUMN IF NOT EXISTS allow_private_pull_sources BOOLEAN NOT NULL DEFAULT FALSE;

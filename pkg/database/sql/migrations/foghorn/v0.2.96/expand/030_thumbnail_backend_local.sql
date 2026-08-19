-- Immutable backend evidence for thumbnails (invariant I2). A thumbnail attempt's destination_cluster is the
-- tenant's OFFICIAL durable cluster, which can be an ALIAS whose advertised S3 backing is actually THIS cell's
-- local bucket (the strict resolver returns StorageMintLocal for it). Cleanup previously decided local-vs-remote
-- by comparing destination_cluster with the local cluster id alone, so such an alias was treated as remote and
-- delegated to federation — which is disabled — leaking the locally-written bytes behind an indefinitely-retrying
-- cleanup. Persist the resolver's local verdict at write time and have both purge and stream cleanup route by it,
-- exactly as foghorn.artifacts.durable_backend_local already does for artifact bytes.
-- Schema source of truth: pkg/database/sql/schema/foghorn.sql.
-- Historical rows are corrected to their known value (local) in the postdeploy backfill
-- postdeploy/005_thumbnail_backend_local_backfill.sql — every existing thumbnail assignment is a local mint, so the
-- ADD COLUMN default of false would otherwise mislabel them remote. New rows are inserted true by ClaimThumbnailAttempt.
ALTER TABLE foghorn.thumbnail_task_assignment
    ADD COLUMN IF NOT EXISTS durable_backend_local BOOLEAN NOT NULL DEFAULT false;

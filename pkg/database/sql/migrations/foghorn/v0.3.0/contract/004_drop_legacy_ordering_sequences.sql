ALTER TABLE foghorn.thumbnail_task_assignment ALTER COLUMN claim_seq DROP DEFAULT;

DROP SEQUENCE IF EXISTS foghorn.node_control_fence_seq;
DROP SEQUENCE IF EXISTS foghorn.source_projection_revision;
DROP SEQUENCE IF EXISTS foghorn.thumbnail_attempt_seq;
DROP SEQUENCE IF EXISTS foghorn.artifact_node_copy_version_seq;
DROP SEQUENCE IF EXISTS foghorn.artifact_catalog_revision_seq;

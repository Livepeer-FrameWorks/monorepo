-- Clean defrost state — postdeploy data migration.
-- After the new code is deployed, no producer writes 'defrosting' anymore.
-- Rewrite any leftover 'defrosting' rows to 's3' (the canonical state for
-- artifacts that live on S3 without a local copy) so the contract migration
-- can drop the index without dangling references.
UPDATE foghorn.artifacts
SET storage_location = 's3',
    updated_at = NOW()
WHERE storage_location = 'defrosting';

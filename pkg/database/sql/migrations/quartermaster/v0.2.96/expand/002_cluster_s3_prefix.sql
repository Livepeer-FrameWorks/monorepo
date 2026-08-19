-- s3_prefix completes the immutable physical S3 backend descriptor on the cluster row. With bucket/endpoint/region it
-- makes Quartermaster the authoritative FULL (bucket, endpoint, region, prefix) tuple, so a Foghorn cell adopts its
-- immutable identity from Quartermaster alone at first boot — no separate Chandler round-trip to learn the prefix, and
-- no boot-time dependency on Chandler serving-readiness. Credentials stay env-only; only the descriptor lives here.
ALTER TABLE quartermaster.infrastructure_clusters
    ADD COLUMN IF NOT EXISTS s3_prefix VARCHAR(255);

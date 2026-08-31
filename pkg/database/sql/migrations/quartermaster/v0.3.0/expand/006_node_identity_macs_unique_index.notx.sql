CREATE UNIQUE INDEX CONCURRENTLY IF NOT EXISTS uq_qm_fingerprints_macs
    ON quartermaster.node_fingerprints(fingerprint_macs_sha256)
    WHERE fingerprint_macs_sha256 IS NOT NULL AND btrim(fingerprint_macs_sha256) <> '';

CREATE UNIQUE INDEX CONCURRENTLY IF NOT EXISTS uq_qm_fingerprints_machine
    ON quartermaster.node_fingerprints(fingerprint_machine_sha256)
    WHERE fingerprint_machine_sha256 IS NOT NULL AND btrim(fingerprint_machine_sha256) <> '';

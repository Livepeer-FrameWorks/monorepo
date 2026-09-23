-- Fingerprint rows uq_qm_fingerprints_macs would reject: rows sharing a MAC-set hash. The partial index skips NULL
-- and blank hashes and compares the stored value exactly, so the grouping does the same.
-- Remediation: frameworks admin nodes fingerprints unbind <node-id> <fingerprint-id> --reason "..."
SELECT f.node_id,
       n.cluster_id,
       f.last_seen,
       f.id AS fingerprint_id,
       f.fingerprint_macs_sha256
  FROM quartermaster.node_fingerprints f
  LEFT JOIN quartermaster.infrastructure_nodes n ON n.node_id = f.node_id
 WHERE f.fingerprint_macs_sha256 IN (
         SELECT d.fingerprint_macs_sha256
           FROM quartermaster.node_fingerprints d
          WHERE d.fingerprint_macs_sha256 IS NOT NULL
            AND btrim(d.fingerprint_macs_sha256) <> ''
          GROUP BY d.fingerprint_macs_sha256
         HAVING count(*) > 1)
 ORDER BY f.fingerprint_macs_sha256, f.last_seen DESC NULLS LAST, f.node_id

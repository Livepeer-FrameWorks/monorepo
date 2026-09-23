package nodeidentity

// ManagedKeylessNodesSQL selects the operator-managed nodes the
// quartermaster_node_identity_keys data migration blocks postdeploy on: active
// gitops_seed / adopted_local nodes in active clusters whose fingerprint lacks a
// 32-byte Ed25519 identity key. Runtime-enrolled nodes are excluded because
// their owners, not the platform operator, re-enroll them.
//
// The Quartermaster verifier and the CLI release report both wrap this query,
// so the release report names exactly the set the gate counts. Columns:
// node_id, cluster_id, tenant_id, enrollment_origin, last_seen; ordered by
// node_id. It reads quartermaster.node_fingerprints.node_identity_public_key_ed25519,
// which exists only once the v0.3.0 expand migrations have run.
const ManagedKeylessNodesSQL = `
SELECT node.node_id,
       node.cluster_id,
       fingerprint.tenant_id::text AS tenant_id,
       node.enrollment_origin,
       GREATEST(node.last_heartbeat, fingerprint.last_seen) AS last_seen
FROM quartermaster.node_fingerprints fingerprint
JOIN quartermaster.infrastructure_nodes node ON node.node_id = fingerprint.node_id
JOIN quartermaster.infrastructure_clusters cluster ON cluster.cluster_id = node.cluster_id
WHERE node.status = 'active'
  AND cluster.is_active = true
  AND node.enrollment_origin IN ('gitops_seed', 'adopted_local')
  AND (
      fingerprint.node_identity_public_key_ed25519 IS NULL
      OR octet_length(fingerprint.node_identity_public_key_ed25519) <> 32
  )
ORDER BY node.node_id`

-- name: UpsertNodeAdmission :one
INSERT INTO foghorn.node_admissions (
    canonical_node_id, fingerprint_sha256, public_key_ed25519, tenant_id, cluster_id,
    validated_at, valid_until, updated_at
) VALUES (
    sqlc.arg(canonical_node_id), sqlc.arg(fingerprint_sha256), sqlc.arg(public_key_ed25519),
    sqlc.arg(tenant_id), sqlc.arg(cluster_id), NOW(), sqlc.arg(valid_until), NOW()
)
ON CONFLICT (canonical_node_id) DO UPDATE SET
    fingerprint_sha256 = EXCLUDED.fingerprint_sha256,
    public_key_ed25519 = EXCLUDED.public_key_ed25519,
    tenant_id = EXCLUDED.tenant_id,
    cluster_id = EXCLUDED.cluster_id,
    validated_at = NOW(),
    valid_until = EXCLUDED.valid_until,
    updated_at = NOW()
RETURNING canonical_node_id;

-- name: DeleteConflictingNodeAdmissions :execrows
DELETE FROM foghorn.node_admissions
WHERE (canonical_node_id = sqlc.arg(canonical_node_id)
       OR fingerprint_sha256 = sqlc.arg(fingerprint_sha256))
  AND NOT (
      canonical_node_id = sqlc.arg(canonical_node_id)
      AND fingerprint_sha256 = sqlc.arg(fingerprint_sha256)
      AND public_key_ed25519 = sqlc.arg(public_key_ed25519)
  );

-- name: GetNodeAdmissionByFingerprint :one
SELECT canonical_node_id, tenant_id, cluster_id, public_key_ed25519, validated_at, valid_until
FROM foghorn.node_admissions
WHERE fingerprint_sha256 = sqlc.arg(fingerprint_sha256)
  AND valid_until > NOW();

-- name: DeleteNodeAdmissionByFingerprintAndKey :execrows
DELETE FROM foghorn.node_admissions
WHERE fingerprint_sha256 = sqlc.arg(fingerprint_sha256)
  AND public_key_ed25519 = sqlc.arg(public_key_ed25519);

-- name: ConsumeNodeAdmissionProofNonce :execrows
INSERT INTO foghorn.node_admission_proof_nonces (
    public_key_sha256, nonce, proof_issued_at, expires_at
) VALUES (
    sqlc.arg(public_key_sha256), sqlc.arg(nonce), sqlc.arg(proof_issued_at), sqlc.arg(expires_at)
)
ON CONFLICT (public_key_sha256, nonce) DO NOTHING;

-- name: DeleteExpiredNodeAdmissionProofNonces :exec
DELETE FROM foghorn.node_admission_proof_nonces WHERE expires_at <= NOW();

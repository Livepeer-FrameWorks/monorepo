-- name: GetInternalCA :one
SELECT role, cert_pem, key_pem, expires_at, created_at, updated_at
FROM navigator.internal_ca
WHERE role = $1;

-- name: SaveInternalCA :one
INSERT INTO navigator.internal_ca (role, cert_pem, key_pem, expires_at, updated_at)
VALUES ($1, $2, $3, $4, NOW())
ON CONFLICT (role) DO UPDATE SET
    cert_pem = EXCLUDED.cert_pem,
    key_pem = EXCLUDED.key_pem,
    expires_at = EXCLUDED.expires_at,
    updated_at = NOW()
RETURNING created_at;

-- name: GetInternalCertificate :one
SELECT id, node_id, cluster_id, service_type, cert_pem, key_pem, expires_at, created_at, updated_at
FROM navigator.internal_certificates
WHERE node_id = $1 AND service_type = $2;

-- name: SaveInternalCertificate :one
INSERT INTO navigator.internal_certificates (node_id, cluster_id, service_type, cert_pem, key_pem, expires_at, updated_at)
VALUES ($1, $2, $3, $4, $5, $6, NOW())
ON CONFLICT (node_id, service_type) DO UPDATE SET
    cluster_id = EXCLUDED.cluster_id,
    cert_pem = EXCLUDED.cert_pem,
    key_pem = EXCLUDED.key_pem,
    expires_at = EXCLUDED.expires_at,
    updated_at = NOW()
RETURNING id, created_at;

-- name: GetTLSBundle :one
SELECT id, bundle_id, domains, cert_pem, key_pem, expires_at, created_at, updated_at, issuer_ca
FROM navigator.tls_bundles
WHERE bundle_id = $1;

-- name: SaveTLSBundle :one
INSERT INTO navigator.tls_bundles (bundle_id, domains, cert_pem, key_pem, expires_at, issuer_ca, updated_at)
VALUES (
    sqlc.arg(bundle_id), sqlc.arg(domains)::jsonb, sqlc.arg(cert_pem), sqlc.arg(key_pem),
    sqlc.arg(expires_at), sqlc.arg(issuer_ca), NOW()
)
ON CONFLICT (bundle_id) DO UPDATE SET
    domains = EXCLUDED.domains,
    cert_pem = EXCLUDED.cert_pem,
    key_pem = EXCLUDED.key_pem,
    expires_at = EXCLUDED.expires_at,
    issuer_ca = EXCLUDED.issuer_ca,
    updated_at = NOW()
RETURNING id, created_at;

-- name: ListExpiringTLSBundles :many
SELECT id, bundle_id, domains, cert_pem, key_pem, expires_at, created_at, updated_at, issuer_ca
FROM navigator.tls_bundles
WHERE expires_at < $1
ORDER BY expires_at ASC;

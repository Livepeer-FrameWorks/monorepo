-- name: GetVerificationUser :one
SELECT id, tenant_id
FROM commodore.users
WHERE verification_token = $1
  AND verified = false
  AND token_expires_at > NOW();

-- name: VerifyUserEmail :exec
UPDATE commodore.users
SET verified = true,
    verification_token = NULL,
    token_expires_at = NULL,
    updated_at = NOW()
WHERE id = $1 AND tenant_id = $2;

-- name: GetVerificationResendUser :one
SELECT id, COALESCE(verified, false)::boolean AS verified, token_expires_at
FROM commodore.users
WHERE email = $1;

-- name: UpdateVerificationToken :exec
UPDATE commodore.users
SET verification_token = $1, token_expires_at = $2, updated_at = NOW()
WHERE id = $3;

-- name: SetPasswordResetToken :exec
UPDATE commodore.users
SET reset_token = $1, reset_token_expires = $2, updated_at = NOW()
WHERE id = $3;

-- name: FindUserByResetToken :one
SELECT id
FROM commodore.users
WHERE reset_token = $1 AND reset_token_expires > NOW();

-- name: ResetUserPassword :exec
UPDATE commodore.users
SET password_hash = $1,
    reset_token = NULL,
    reset_token_expires = NULL,
    updated_at = NOW()
WHERE id = $2;

-- name: UpdateUserFirstName :exec
UPDATE commodore.users
SET first_name = $1, updated_at = NOW()
WHERE id = $2 AND tenant_id = $3;

-- name: UpdateUserLastName :exec
UPDATE commodore.users
SET last_name = $1, updated_at = NOW()
WHERE id = $2 AND tenant_id = $3;

-- name: UpdateUserName :exec
UPDATE commodore.users
SET first_name = $1, last_name = $2, updated_at = NOW()
WHERE id = $3 AND tenant_id = $4;

-- name: GetNewsletterUser :one
SELECT email, first_name, last_name
FROM commodore.users
WHERE id = $1 AND tenant_id = $2;

-- name: GetUserEmail :one
SELECT email
FROM commodore.users
WHERE id = $1 AND tenant_id = $2;

-- name: InsertWalletChallenge :exec
INSERT INTO commodore.wallet_auth_challenges (
    wallet_address, chain_id, message_hash, expires_at
)
VALUES ($1, $2, $3, $4);

-- name: ConsumeWalletChallenge :one
UPDATE commodore.wallet_auth_challenges
SET consumed_at = NOW()
WHERE wallet_address = $1
  AND message_hash = $2
  AND consumed_at IS NULL
  AND expires_at > NOW()
RETURNING id;

-- name: InsertLinkedWallet :one
INSERT INTO commodore.wallet_identities (tenant_id, user_id, chain_type, wallet_address)
VALUES ($1, $2, 'ethereum', $3)
RETURNING id, created_at;

-- name: LockUserAuthenticationMethods :one
SELECT COALESCE(password_hash, '') <> ''
       AND email IS NOT NULL
       AND verified = TRUE AS has_password_signin
FROM commodore.users
WHERE id = $1 AND tenant_id = $2
FOR UPDATE;

-- name: UserOwnsWallet :one
SELECT EXISTS (
    SELECT 1
    FROM commodore.wallet_identities
    WHERE id = $1 AND user_id = $2 AND tenant_id = $3
) AS owned;

-- name: CountUserWallets :one
SELECT COUNT(*)::integer
FROM commodore.wallet_identities
WHERE user_id = $1 AND tenant_id = $2;

-- name: DeleteUserWallet :one
DELETE FROM commodore.wallet_identities
WHERE id = $1 AND user_id = $2 AND tenant_id = $3
RETURNING id;

-- name: FindOtherUserIDByEmail :one
SELECT id
FROM commodore.users
WHERE LOWER(email::text) = $1 AND id != $2;

-- name: LinkUserEmail :exec
UPDATE commodore.users
SET email = $1,
    password_hash = $2,
    verification_token = $3,
    token_expires_at = $4,
    verified = false,
    updated_at = NOW()
WHERE id = $5 AND tenant_id = $6;

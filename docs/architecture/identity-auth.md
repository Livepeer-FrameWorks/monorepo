# Identity and authentication

Commodore owns users, interactive sign-ins, refresh-token chains, wallet
identities and account recovery. Bridge exposes HTTP and GraphQL authentication
and passes verified identity to service clients. Tenant roles and the separate
platform-operator grant come from signed claims; a tenant owner is not a
platform operator.

## Credential types

| Credential                | Lifetime                                               | State and authority                                                                          |
| ------------------------- | ------------------------------------------------------ | -------------------------------------------------------------------------------------------- |
| Interactive access JWT    | Up to 15 minutes                                       | Names a Commodore `auth_sessions` row with `sid`; retains the original `auth_time`           |
| Interactive refresh token | 30-day idle limit, bounded by the session maximum      | Opaque secret stored as a SHA-256 hash; one session owns the rotation chain                  |
| API token                 | Token-specific                                         | Validated against its owner; internal RPC delegation has its own audience and short lifetime |
| Wallet bearer handoff     | 15 minutes, no refresh                                 | Bridge audience, linked wallet and active owner required; creates no session or refresh rows |
| Internal wallet assertion | At most one minute and no later than the bearer expiry | Single service audience and a fresh, one-use JTI; no platform-operator grant                 |
| Service credential        | Service configuration                                  | Service-only RPC authorization; never treated as a user's interactive sign-in                |

Browsers have a 90-day absolute session maximum. CLI and Mac tray sessions have
a 180-day maximum. Unknown sessions adopted from legacy refresh chains use the
conservative 90-day maximum. Android is not an implemented client.

Commodore verifies wallet proofs through `VerifyWalletAuth`; Bridge then mints
the MCP handoff with `GenerateWalletBearerJWT`, using its existing JWT signing
secret. This programmatic bearer has no `sid` or refresh token. It is separate
from Commodore's interactive issuer and can be ended early by the user-wide
revocation cutoff, account deactivation or wallet unlinking.

## Interactive issuance

Password login, explicit wallet login, PKCE exchange and approved device-code
pickup all use `issueUserSessionTx`. The issuer locks the user, reads current
account state, enforces the 100-live-session limit and writes the session,
refresh hash and sign-in event in one transaction. At the limit it revokes the
oldest idle session. Password login also rechecks the verified password hash
under that lock so a concurrent reset cannot turn an old proof into a new
session.

`auth_sessions` is scoped by `(tenant_id, user_id, id)`. It records the client,
authentication method, device label, client version, IP and user agent, original
creation time, last refresh, idle expiry, absolute expiry and revocation reason.
The refresh-token foreign key includes the complete owner. Database time bounds
new deadlines, including when the application clock runs ahead or a transaction
waits for a lock. Access-token expiry never extends past the session deadline.

Bridge captures request IP using its trusted-proxy configuration. Client labels,
versions and user agents are bounded display hints and grant no authority.
Binary gRPC metadata preserves Unicode without using gRPC's own user-agent as
the end user's browser identity.

## Rotation

Native clients send a JSON refresh grant to `POST /auth/oauth/token`; browsers
use the HttpOnly-cookie `POST /auth/refresh` path. Native responses are
`no-store` and do not change browser cookies. A definitive rejection is
`invalid_grant`; service failures and concurrent rotation losers are retryable.

Refresh locks the owner and presented token, resolves or adopts its named
session, verifies the session and account, and rotates within one transaction.
Every successor keeps the same `sid` and original `auth_time`. A grace replay
returns a retryable error without creating another head or clearing the winning
client's credentials. After the ten-minute grace period, an unused successor
can be retired and replaced to recover a lost response. Reuse of a token whose
successor has been used revokes that session only.

Runtime adoption and the release data migration preserve the oldest available
creation time in a legacy chain. A successor written without `sid` inherits an
existing predecessor's session rather than restarting its maximum age.

## Revocation

`UserService` provides self-service `ListSessions`, `RenameSession`,
`RevokeSession` and `RevokeOtherSessions`. These require an interactive JWT;
API tokens, wallet handoff bearers and service credentials cannot manage a
user's interactive sessions. The owner always comes from verified context,
including for platform operators. Logout revokes the current named session.

`ChangePassword` requires an interactive named session and proof of the current
password. It rechecks that proof under the owner lock, changes the password,
invalidates outstanding password-reset tokens and revokes other sessions in one
transaction. The current access token and refresh family remain usable, including
a sid-less successor written by an older issuer. A concurrent password change
cannot reuse a proof against the previous hash. Failed revocation-event storage
rolls the credential change and revocations back together.

Named revocation deletes only the connected refresh family and emits
`auth_session.revoked`. It locks existing chain rows before deleting, then uses
a fresh statement snapshot to include an in-flight older issuer's sid-less
successor. Deletion makes older issuers return an invalid grant instead of
misinterpreting an explicit sign-out as account-wide token theft.

Password reset and account deactivation retain the user-wide cutoff and
`user.sessions_revoked` event. They also revoke named session rows. Global
refresh invalidation waits for old rotations before its mutation; the cutoff
uses wall-clock time after that wait, rather than transaction-start time.
This rejects access minted by an old rotation while the reset was waiting.

New Bridges opt into named revocation backfill. Older Bridges receive only
user-wide events; a named revocation never becomes an account-wide sign-out
through an older event consumer. Bridge caches use the full owner and session
ID, and close only matching WebSockets for named revocation.

State validation is wired into Bridge authentication and network diagnostics,
Commodore unary/stream authentication, Foghorn internal HTTP, and Skipper's
gRPC, REST and notification MCP requests. Coverage of other raw-JWT consumers
and ongoing stream revalidation is tracked in the account-security RFC.

## Native credential ownership and compatibility

CLI storage uses `cli_session`, `cli_refresh` and `cli_bridge_url`; the tray
owns separate credentials. CLI renewal is serialized across processes and
stays bound to the original Bridge. `FW_USER_TOKEN` belongs to the invoking
process and is neither refreshed nor overwritten. CLI logout revokes the
stored CLI session, even when that invocation has a borrowed environment token.
Failures retain credentials for retry; `logout --local` explicitly clears only
local storage.

The tray binds its stored access and refresh credentials to its login Bridge.
Requests and child CLI handoffs cannot use them after a context switch to a
different Bridge. Credentials saved without an origin require a fresh sign-in.
Refreshes are coalesced, redirects are refused, outages retain credentials and
definitive rejection clears them locally. An automatic recovery failure never
calls server logout.

Manual browser and tray logout waits for successful server revocation before
discarding credentials. Expired access is renewed once; outages keep credentials
available for another attempt and display an error. Tray logout waits for an
in-flight refresh to finish so it cannot discard a committed rotation's result.
The tray freezes the destination Bridge for the logout request.

A valid legacy browser token without a session ID receives HTTP 409 with
`SESSION_REFRESH_REQUIRED` from logout, without cookie changes. The browser
renews the session and retries named logout. Automatic session-end handling
clears local state without sending another logout request.

CLI and tray retry the legacy cookie refresh path only for the older Bridge's
HTTP 400 `invalid request` PKCE validation response. Invalid grants, unsupported
grants, outages and redirects never select that path. Credentials without a
named session use local sign-out against old platforms, avoiding their global
logout semantics.

## Release state

The pending `v0.3.15` expand migration adds the session table, nullable refresh
ownership and authorization-code metadata. Existing-table indexes build online
and are validated after deployment. The service-owned
`commodore_auth_sessions_v0_3_15` data migration must finish before contract.
No session-ownership `NOT NULL` contract ships in the foundation migration.

The self-service UI, security email, activity reader, password-change UI,
authorization consent metadata and retention sweep remain tracked in
[the account-security RFC](../rfcs/RFC_account_security_sessions.md).

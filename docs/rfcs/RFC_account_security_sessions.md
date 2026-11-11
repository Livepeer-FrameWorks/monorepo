# RFC: Account security — signed-in sessions

Status: approved; implementation in progress (native refresh compatibility and session foundations implemented)
Owner: Commodore (`api_control`), Bridge (`api_gateway`), webapp, CLI, Mac tray

## Problem

A user can sign in from a browser, the CLI (device-code grant), the Mac tray
(PKCE loopback) or a wallet. None of those sign-ins
can be seen or revoked individually. The only revocation is "end every session
of this user", and it fires on every web or tray logout. A lost laptop's CLI
login can only be killed by signing out everywhere or resetting the password,
and no page tells the user that.

The experimental `app_android/` scaffold is outside this feature's scope. Its
README identifies FrameWorks integration as placeholder/mock code; it is not a
supported sign-in client.

## Pre-implementation state (research results)

### Data model

- There is no session object. A sign-in is a chain of `commodore.refresh_tokens`
  rows (`pkg/database/sql/schema/commodore.sql:66`): `id, tenant_id, user_id,
token_hash, expires_at, created_at, rotated_at, revoked, replaced_by`.
  `InsertRefreshToken` (`api_control/internal/database/queries/account_sessions.sql:50`)
  writes only `tenant_id, user_id, token_hash, expires_at`.
- No client id, auth method, device label, IP, user agent, `last_used_at` or
  stable family id. `created_at` is the time of the last rotation, not the
  sign-in.
- Rotation inserts a new row with a fresh 30-day expiry (sliding, no absolute
  cap). A replay inside the 10-minute grace (`server.go:9024`) issues an
  unlinked pair, forking a second live chain for one logical session.
- Client identity is known at issue time and then thrown away:
  `auth_device_codes` rows carry `client_id`/`scope` and are deleted on pickup
  (`server.go:5828`); `auth_authorization_codes` keep `client_id` but are never
  linked to the refresh row.
- Session JWT claims (`pkg/auth/jwt.go:38`): `user_id, tenant_id, email, role,
roles, auth_time, iat, exp`. No `sid`/`jti`. `auth_time` is set to
  `time.Now()` on every refresh, so it never reflects the original sign-in.
- No cleanup of expired/rotated `refresh_tokens`, consumed
  `auth_authorization_codes` or expired `auth_device_codes`; no `expires_at`
  index on `refresh_tokens`; no per-user cap.

### Session-issuing paths

| Path                        | Code                                         | Auth type   | Client known                   | Notes                                                                                                       |
| --------------------------- | -------------------------------------------- | ----------- | ------------------------------ | ----------------------------------------------------------------------------------------------------------- |
| Password                    | `Login` `server.go:4770`                     | password    | no (web)                       | builds its own JWT + row instead of `issueUserSessionTx`                                                    |
| Wallet REST                 | `WalletLogin` `server.go:6466`               | wallet      | no                             | own inline JWT + row; event sends `wallet_id=""`                                                            |
| Wallet GraphQL              | `resolvers/auth.go:120`                      | wallet      | no                             | returns only the access token; refresh row orphaned for 30 days                                             |
| Wallet headers (agents/MCP) | `middleware/auth_request.go:80`              | wallet      | no                             | calls `WalletLogin` **per request**: one refresh row + one `auth_login_succeeded` per authenticated request |
| PKCE (tray)                 | `ExchangeAuthorizationCode` `server.go:5472` | pkce        | `tray-mac` on code row         | not linked to the session                                                                                   |
| Device code (CLI)           | `PollDeviceAuthorization` `server.go:5756`   | device_code | `cli`/`tray-mac` on device row | row deleted on pickup                                                                                       |

### Revocation

- Per user only: `users.sessions_revoked_at` + internal event
  `user.sessions_revoked` (reasons LOGOUT, PASSWORD_RESET, REFRESH_TOKEN_REUSE,
  OPERATOR_DEACTIVATION). Bridge enforces it through
  `middleware.EnforceSessionRevocations` fed by Signalman `CHANNEL_SESSIONS`
  with `ListRecentSessionRevocations` backfill; WebSockets close at once and
  revalidate every minute.
- Not enforced in Commodore's gRPC interceptor (`pkg/middleware/grpc.go:119`),
  Foghorn (`api_balancing/internal/handlers/internal_auth.go:62`), Skipper
  (`api_consultant/cmd/skipper/main.go:824`) or `handlers/network.go:102`.
  Those accept a revoked JWT for its remaining 15 minutes.
- `Logout` (`server.go:5138`) ignores the presented token, deletes every refresh
  row of the user and revokes all sessions. Web and tray logout therefore sign
  out the browser, CLI and tray together.
- Refresh reuse after the grace window revokes **all** of the user's sessions
  (`server.go:5253`), not the affected chain.
- `ResetPassword` revokes all sessions. `LinkEmail` (sets a password on a
  wallet account) revokes nothing. There is no change-password-while-signed-in.

### Clients

- **CLI** (`cli/cmd/login.go`): device grant sends only `{client_id: "cli",
scope}` and `User-Agent: FrameWorks-CLI/1.0`. It stores the refresh token but
  **never refreshes** (`cli/internal/credentials/store.go:14`): a CLI login
  works for 15 minutes unless the Mac tray happens to refresh the shared
  keychain entry. `frameworks logout` deletes local entries only; the server
  session stays valid for 30 days. No `whoami`/status/sessions command.
- **Mac tray** (`app_mac/Sources/Gateway/AuthService.swift`): PKCE loopback,
  refreshes every 12 minutes by forging a `Cookie: refresh_token=` header
  because `/auth/refresh` reads only the cookie. Shares keychain service
  `com.livepeer.frameworks` accounts `user_session`/`refresh_token` with the
  CLI, so the two are one credential and each one's logout logs out the other.
- **Webapp**: cookie session, refresh loop every 10 minutes with cross-tab Web
  Locks (`lib/stores/auth.ts`, `lib/auth/refresh.ts`). Logout is a top-bar
  button. `/settings` holds profile, appearance, newsletter, linked wallets
  (unlink without confirmation), link email, billing details, account info.
  No security section, password change, sign-out-everywhere, activity log or
  session list.
- **Consent pages**: `/device` shows only client and scope, is hard-coded to
  "Authorize CLI" although `tray-mac` is also accepted, and never shows the
  fetched `expires_at`. `/authorize` shows client and scope. Neither shows which
  machine asked, from where, or when — the information that makes a phished
  device code recognisable.

### Audit trail

`auth_login_succeeded`, `auth_login_failed`, `auth_token_refreshed`, etc. flow
through the Commodore outbox to ClickHouse `api_events`
(`api_analytics_ingest/internal/handlers/handlers.go:388`). `AuthEvent.ip_bucket`
is never set; the client IP never reaches Commodore because the gRPC client
forwards only `authorization, x-user-id, x-tenant-id, x-demo-mode, x-payment`
(`pkg/clients/commodore/grpc_client.go:114`); user agent is not forwarded. No
user-facing reader exists.

### Docs and registry

- No end-user page on signing in, session lifetime, logout semantics or
  recovering from a lost device. `docs/architecture/module-map.md:56` lists
  identity/auth with doc home "NONE".
- `cli-reference.mdx:3806-3832` describes `frameworks login` as using "your user
  session token" (it runs a device-code grant) and omits `--no-browser`,
  `--token`, `--timeout`, `--bridge-url`; logout is described without saying the
  server session survives.
- `builders/api-reference.mdx:99` correctly states that signing out ends every
  session.
- Lifetimes (15-minute access token, 30-day sliding refresh, 10-minute reuse
  grace) appear in no doc.
- `docs/platform-features.yaml` has no account-security row; `tenant` says
  "account security controls are not there yet"; `mac-tray` is deliberately
  undocumented.

## Defects found (independent of the feature)

These are wrong today and get fixed regardless of the UI:

1. CLI never refreshes, so device-code logins die after 15 minutes.
2. CLI logout leaves the server session alive for 30 days.
3. Wallet header auth mints a refresh row and a login event on every request;
   GraphQL `walletLogin` mints a refresh row it never returns.
4. Refresh-token reuse revokes every session of the user instead of the
   compromised chain.
5. Grace-window replays fork unlinked chains.
6. `auth_time` is reset on every refresh.
7. Revoked JWTs are accepted by Commodore, Foghorn, Skipper and
   `handlers/network.go` for up to 15 minutes.
8. `LinkEmail` sets a password without ending other sessions.
9. No cleanup of expired refresh rows and auth codes.
10. `/device` labels every client "Authorize CLI"; client IP and user agent
    never reach Commodore; `AuthEvent.wallet_id`/`ip_bucket` are empty.
11. `/auth/refresh` accepts only a cookie, so native clients forge one.

## Design

### Principle

A session is a first-class Commodore object created once per interactive
sign-in. Every access token names its session, every refresh token belongs to
exactly one session, and every revocation targets sessions. "Sign out
everywhere" is the set of all sessions, not a separate mechanism.

### Data (Commodore)

New table `commodore.auth_sessions`:

| Column                                 | Notes                                                                                          |
| -------------------------------------- | ---------------------------------------------------------------------------------------------- |
| `id UUID`                              | the `sid`                                                                                      |
| `tenant_id, user_id`                   | tenant-filtered like every other query                                                         |
| `auth_method`                          | `password`, `wallet`, `pkce`, `device_code`                                                    |
| `client_id`                            | `web`, `cli`, `tray-mac` (closed set, validated)                                               |
| `device_label`                         | from the client (hostname/OS for native, parsed UA for web); user can rename                   |
| `client_version`                       | CLI/tray/app version when sent                                                                 |
| `created_ip`, `last_ip`                | full IP stored, shown masked; location via `pkg/geoip` at read time                            |
| `user_agent`                           | raw, truncated                                                                                 |
| `created_at, last_seen_at`             | `last_seen_at` bumped on refresh, not per request                                              |
| `idle_expires_at, absolute_expires_at` | see Lifetimes                                                                                  |
| `revoked_at, revoke_reason`            | `user`, `logout`, `password_change`, `password_reset`, `reuse_detected`, `operator`, `expired` |

`refresh_tokens` gains `session_id NOT NULL` (expand/contract). Rotation and
grace-window replays keep the `session_id`. Rotation must also serialize to one
live successor per token; a shared session id alone does not prevent forks. A
concurrent loser must receive a retryable response or the same usable successor,
without creating another chain or destroying the winning client's credentials.
`auth_device_codes` and `auth_authorization_codes` gain `device_label`,
`client_version`, `request_ip`, `user_agent` so the approval page can show them
and the session inherits them.

Backfill: every live chain head becomes one session with
`client_id = 'unknown'`, `device_label = 'Signed in before sessions were tracked'`.

Retention: a Commodore sweep deletes sessions (and their refresh rows) 30 days
after revocation or expiry, deletes consumed/expired auth codes after one day,
with `expires_at` indexes. Hard cap of 100 live sessions per user; the oldest
idle one is revoked when exceeded.

### Tokens and revocation

- Session JWT gains `sid`; `auth_time` carries the session's `created_at`.
- Per-session revocations identify `tenant_id`, `user_id`, `session_id` and
  `revoked_at`; Bridge's cache keys by the full owner and `sid`. The existing
  user-wide cutoff remains for legacy JWTs during rollout. Account deactivation
  stays a user-level fact (`is_active`).
- `auth_session.revoked` is a distinct internal event on `CHANNEL_SESSIONS`.
  `user.sessions_revoked` retains its user-wide meaning. Older Bridges ignore
  the new event instead of accidentally signing out every device.
- `ListRecentSessionRevocations` returns named sessions only when the caller
  explicitly sets `include_auth_sessions`. New clients opt in; old clients
  receive only the user-wide revocations they understand.
- Revocation checking moves into the shared JWT validation path used by Bridge,
  Commodore, Foghorn, Skipper and `handlers/network.go`, so no service accepts
  a revoked session.
- WebSockets close per session (`CloseSession(sid)`) next to the existing
  `CloseAPIToken`.
- Refresh reuse revokes the affected session only and records
  `reuse_detected` (OAuth security BCP behaviour), plus a security email
  (below).

### Who mints sessions

- `issueUserSessionTx` becomes the only issuer; `Login` and `WalletLogin` stop
  building their own JWT/row. It takes a `SessionContext{auth_method,
client_id, device_label, client_version, ip, user_agent}`.
- The Commodore gRPC client forwards `x-client-ip` and `user-agent` from Bridge
  (the IP is already in `ctxkeys.KeyClientIP`).
- Per-request wallet header auth stops calling `WalletLogin`; it uses a
  verify-only RPC that returns the user without minting anything. MCP retains
  its `X-Access-Token` handoff as a 15-minute, non-refreshable wallet bearer,
  separate from interactive sessions and internal RPC assertions. Header
  messages are base64-encoded UTF-8 so the signed SIWE bytes survive HTTP.
  GraphQL
  `walletLogin` either returns the refresh token or doesn't mint one; it
  returns it, so wallet clients can refresh like every other client.

### Lifetimes

- Access token 15 minutes (unchanged).
- Refresh idle timeout 30 days (unchanged behaviour).
- New absolute lifetime per session: 90 days for `web`, 180 days for
  `cli`/`tray-mac`, after which the user signs in again.

### Refresh endpoint for native clients

Add `grant_type=refresh_token` to `POST /auth/oauth/token` (RFC 6749 §6),
body in, JSON out. CLI and tray use it; the cookie forging in the tray
goes away. Web keeps cookie `/auth/refresh`.

### API

gRPC `UserService` (interactive callers only, via `sessionOwner`; programmatic
wallet bearers, API tokens and service credentials are refused):

- `ListSessions(cursor)` → `AuthSession[]`, current session flagged by `sid`
- `RevokeSession(session_id)` (idempotent; revoking the current one = logout)
- `RevokeOtherSessions()`
- `RenameSession(session_id, label)`
- `ChangePassword(current_password, new_password)` → revokes all other sessions
- `Logout` revokes only the presented session (`sid`), not all

GraphQL mirrors it under the signed-in user:

```graphql
type AuthSession {
  id: ID!
  clientKind: AuthClientKind!   # BROWSER, CLI, DESKTOP, UNKNOWN
  clientId: String!
  authMethod: AuthMethod!       # PASSWORD, WALLET, BROWSER_HANDOFF, DEVICE_CODE
  label: String!
  clientVersion: String
  ipAddress: String             # masked, e.g. 203.0.113.x
  location: String              # city/country from GeoIP
  createdAt: Time!
  lastActiveAt: Time!
  expiresAt: Time!
  current: Boolean!
}

authSessionsConnection(first: Int, after: String): AuthSessionConnection!
revokeAuthSession(id: ID!): RevokeAuthSessionResult!
revokeOtherAuthSessions: RevokeOtherAuthSessionsResult!
renameAuthSession(id: ID!, label: String!): RenameAuthSessionResult!
changePassword(currentPassword: String!, newPassword: String!): ChangePasswordResult!
securityActivityConnection(first: Int, after: String): SecurityActivityConnection!
```

`securityActivityConnection` reads the user's own auth events (sign-in
succeeded/failed, session revoked, password changed/reset, wallet
linked/unlinked) from `api_events` through Periscope, filtered by
`tenant_id` and `user_id`, 90 days.

Names use "auth session" throughout: "Sessions" alone already means viewer
sessions and "Devices" already means AV devices (`/devices`).

### Webapp

New page **Account → Security** (`/account/security`), linked in the Account
nav section and from `/settings`:

1. **Signed-in sessions** slab: table (icon by client kind, label, method,
   location + masked IP, last active, created; "This session" badge). Row
   actions: Rename, Sign out. Header action: "Sign out all other sessions".
   Confirmation uses the slab `Dialog` pattern (`DeleteStreamModal`), not
   `confirm()`.
2. **Password** slab: change password (current + new); for wallet-only users
   this is the existing Link Email flow, moved here.
3. **Sign-in methods** slab: linked wallets moved from `/settings`, with a
   confirmation dialog on unlink.
4. **Recent security activity** slab: last 90 days from
   `securityActivityConnection`.

Top-bar Logout signs out this session only. `/device` and `/authorize` become
client-neutral ("Sign in FrameWorks CLI on _marco-mbp_") and show device
label, OS/version, requesting IP + location, request time and expiry
countdown, with "Only approve if you started this sign-in yourself."

### CLI

- `frameworks login` sends `device_label` (hostname), OS and CLI version to
  `/auth/device/start`.
- The CLI refreshes through `/auth/oauth/token` before expiry and on 401.
- The CLI uses its own keychain accounts (`cli_session`, `cli_refresh`), so the
  CLI and the tray are separate sessions with separate logouts. The tray keeps
  passing its own token to child CLI processes via `FW_USER_TOKEN`.
- `frameworks logout` revokes the server session, then clears local storage.
- New: `frameworks auth status` (who, tenant, session label, expiry) and
  `frameworks auth sessions list|revoke <id>|revoke-others`.

### Mac tray

- Tray sends device label and version on `/authorize`, refreshes via
  `/auth/oauth/token`, logout revokes only its own session. A Bridge returning
  HTTP 400 with the legacy `invalid request` validation response triggers
  cookie `/auth/refresh` compatibility; token rejections, unsupported grants
  and service/transport failures do not trigger that fallback.

### Security email

New-session email through the existing `account_email_outbox`: sent when a
session is created with a `(client_id, device_label)` the user has not used in
90 days, and on `reuse_detected`. It names device, location, time and links to
`/account/security`.

### Events

Internal `auth_session.revoked` carries the session ID; `user.sessions_revoked`
retains its user-wide meaning. Both reach service-only `CHANNEL_SESSIONS`; the
new type is also included in the tenant audit table. No public webhook event;
security events stay user-facing in the webapp and email.

## Docs

- New `website_docs/.../builders/account-security.mdx`: how sign-in works per
  client, lifetimes, what logout does, managing sessions, recovering from a
  lost device, changing password.
- Rewrite `cli-reference.mdx` login/logout (device grant, flags, server-side
  revoke) and add `auth status` / `auth sessions`.
- Update `builders/api-reference.mdx:93-115` (logout ends one session; new
  close reasons) and `builders/troubleshooting.mdx:166-184` (lifetimes).
- New `docs/architecture/identity-auth.md` (session model, issuers, rotation,
  revocation fan-out, lifetimes) and replace "NONE" in `module-map.md:56`.
- Registry: new `account-security` row (area account) with docs, webapp,
  GraphQL and CLI surfaces; drop the "account security controls" gap from
  `tenant`.

## Release

The latest tag is `v0.3.14`; the catalog now declares pending `v0.3.15`.
Expand adds `auth_sessions`, nullable `refresh_tokens.session_id`, and code-table
metadata. Indexes on existing tables build concurrently and are checked in
postdeploy, alongside validation of the refresh-token/session ownership FK.

The service-owned `commodore_auth_sessions_v0_3_15` data migration backfills
legacy chains in bounded tenant batches and is required before contract.
Backfilled `unknown` sessions use the conservative 90-day lifetime from the
oldest available token in the chain. A head written by an old issuer inherits
an existing session from its predecessors. No bulk data rewrite runs in expand.
Contract may set `session_id NOT NULL` only after issuers are migrated and
expired/orphaned legacy rows have been cleaned up; no contract migration ships
in the foundation slice.

Retain `users.sessions_revoked_at` and its checks through the legacy-token
window: old JWTs without `sid` keep the existing user-level revocation and
deactivation protection until they expire. Drop the legacy column only after
no old issuer remains and its last JWT has expired.

## Tests

- Stack scenario: sign in web + CLI device grant + tray PKCE; list shows three
  sessions with labels; revoke CLI → CLI 401 within seconds, web unaffected;
  web logout → only web ends; sign out others → only current survives;
  change password → others end.
- Stack scenario: CLI session survives past 15 minutes (refresh), CLI logout
  makes the refresh token fail server-side.
- Stack scenario: refresh reuse revokes one session, not the others.
- Stack scenario: revoked JWT refused by Commodore, Foghorn and Skipper paths.
- Unit: wallet header auth creates no refresh rows; GraphQL `walletLogin`
  returns a refresh token; sweep deletes expired rows; session cap.
- Component tests for the Security page and the consent pages.

## Decisions (maintainer, 2026-11-11)

1. Absolute session lifetime: 90 days for `web`, 180 days for
   `cli`/`tray-mac`.
2. New-session and reuse-detected email: always on, no opt-out.
3. Owner/admin management of members' sessions ships with `team-rbac` and the
   Team pages; this feature is self-service only, with the authorization check
   isolated so widening it later is a rule change.
4. Preserve the short-lived MCP bearer handoff after wallet proof verification;
   it creates no session or refresh rows. Explicit wallet login creates a
   refreshable session.

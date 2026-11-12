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
session. Wallet issuance resolves the verified address to its owned identity
under the same lock; an unlinked wallet cannot finish a stale sign-in proof.

`auth_sessions` is scoped by `(tenant_id, user_id, id)`. It records the client,
authentication method, device label, client version, IP and user agent, original
creation time, last refresh, idle expiry, absolute expiry and revocation reason.
The refresh-token foreign key includes the complete owner. Database time bounds
new deadlines, including when the application clock runs ahead or a transaction
waits for a lock. Access-token expiry never extends past the session deadline.

Issuance samples at most 120 live rows. At the limit it rolls back the grant
transaction, then commits owner-locked cleanup steps before replaying the
credential check and issuance. Each step reads at most 20 unrevoked LRU
candidates through the owner index. It marks already-expired candidates terminal
at their original deadline, using the same database clock as the live count,
without emitting active-session revocations. This progress survives cancellation
and prevents old expired history from turning a bounded eviction into a scan.
Live candidates receive named revocation and mixed-version refresh-family cleanup,
without a user-wide cutoff. A transaction changes at most 20 candidate rows.
Cancellation preserves the unconsumed grant; retry continues from committed
cleanup. A legacy refresh that creates a session uses the same cap and cleanup
budget. The credential being adopted is excluded from eviction, and its original
sign-in time and absolute expiry remain intact.

After attaching legacy refresh families, the backfill walks users through an
explicit range index on their IDs and normalizes caps with the same cleanup.
It checkpoints only completed owners; new unassigned heads reset that pass.
The shared named revocation and refresh-family finalizer serves RPCs and
migration work, with the migration recorded as a service actor. Verification
checks the cap using bounded owner reads, and dry-run reports a bounded
read-only preview instead of treating “no unassigned heads” as completion.

Device-code grants persist bounded device label, client/platform version, source IP
and user agent when the native client starts the request. Authenticated lookup
returns that original metadata and its creation/expiry times for consent.
Redemption inherits the stored values; later polls and browser approval do not
replace them. Older pending grants without metadata keep generic client labels
and unknown addresses rather than attributing the polling request.

Before opening browser approval, a current tray calls Bridge's anonymous
`/auth/authorize/start`. Bridge signs its observed native address, user agent,
device label and version together with the exact client, loopback redirect,
S256 challenge, scope and state. The two-part context expires after five minutes;
it carries no user identity, cannot authenticate an API request and creates no
session or refresh rows. HMAC domain separation keeps it distinct from JWTs and
other proofs signed with Bridge's existing signing secret.

Authenticated `/auth/authorize/lookup` verifies that context for consent display.
Completion refuses changed bindings or an expired context and forwards the
original native metadata while retaining the browser JWT's user and tenant.
Commodore stores approved metadata on the authorization code and inherits it on
exchange, so redemption cannot replace what was approved. Older trays retain
legacy PKCE approval without verified native provenance; a current tray falls
back to that flow only when the Bridge lacks the start endpoint (404/405/501).
Rejections, redirects, malformed success and outages do not select fallback.

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

GraphQL exposes `authSessionsConnection(first, after)`, `renameAuthSession`,
`revokeAuthSession`, `revokeOtherAuthSessions` and `changePassword`. The
connection accepts 1–100 rows and returns only the caller's live sessions.
Addresses are masked to three IPv4 octets or an IPv6 /48; a configured local
`GEOIP_MMDB_PATH` supplies optional city/country display. Full addresses and raw
user agents are not exposed. Revoking the presented session returns
`current: true` so clients can discard its credentials.

Revoke-others, password change and email linking require the presented named
session to be preserved. Legacy JWTs without `sid` receive a typed
`SESSION_REFRESH_REQUIRED` validation result before an RPC; clients renew once
and retry. Programmatic credentials receive an authentication refusal.

`ChangePassword` requires an interactive named session and proof of the current
password. It rechecks that proof under the owner lock, changes the password,
invalidates outstanding password-reset tokens and revokes other sessions in one
transaction. The current access token and refresh family remain usable, including
a sid-less successor written by an older issuer. A concurrent password change
cannot reuse a proof against the previous hash. Failed revocation-event storage
rolls the credential change and revocations back together.

`LinkEmail` creates password credentials for an interactive wallet-only account
under the same owner lock. It preserves the current named session and its
legacy refresh successor, revokes other sessions, invalidates reset proofs and
queues verification delivery in `account_email_outbox` in one transaction.
Concurrent attempts cannot replace the first linked email. A delivery/event
persistence failure rolls the credential change and revocations back together;
SMTP availability is handled by the existing delivery worker.

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

Named cache entries remain for a full access-token lifetime plus clock leeway
after receipt. A transaction-start revocation timestamp can precede issuance
while the revoker waits for a lock; eviction must not let a still-verifiable
token work again. User-wide cutoffs keep their actual issue-time boundary so
later legitimate sign-ins survive delayed delivery.

Bridge authentication and network diagnostics, Foghorn internal HTTP, Skipper's
gRPC/REST/notification MCP paths, and the raw interactive gRPC ingresses in
Quartermaster, Purser, Periscope Query, Signalman, Bosun and Lookout each use
replica-local `auth.SessionRevocations`. Validating an access JWT does not make
a Commodore RPC or load a missing session from the database. Commodore retains
its own in-process account/session checks for authoritative operations. Service
tokens, audience-bound delegation and wallet bearer verification keep their
credential-specific rules.

The shared `pkg/sessionrevocations` feed receives every tenant's typed revocation
events over one service-authenticated, tenantless Signalman `CHANNEL_SESSIONS`
stream per replica. Signalman applies its existing domain-event consumer's
revocations locally before broadcasting them, without subscribing to itself.
Bridge reuses its confirmed upstream subscription and applies shared backfill
results to both its HTTP cache and WebSocket closures.

Background snapshots start at boot, after every confirmed stream reconnection,
and on a jittered one-minute reconciliation interval. They opt into named
revocations with `include_auth_sessions`, span access-token lifetime plus clock
leeway, and merge monotonically with live events. A snapshot includes
`auth_sessions_included=true` only after Commodore read the named-session state;
an older response still supplies legacy cutoffs but cannot establish complete
named coverage. Snapshot failures retry with bounded exponential backoff.

Loss of Commodore or the event stream preserves known revocations and serves
otherwise locally valid access credentials, including before a cold replica
finishes hydrating. When neither path can deliver a new revocation, that fact
cannot be enforced until recovery or signed JWT expiry (at most fifteen minutes
plus clock leeway). Issuance, refresh and account/session mutations still need
Commodore. Synchronization metrics expose background degradation; it does not
make ordinary authentication unavailable.

Interactive gRPC streams end at JWT expiry and recheck the same local cache
every thirty seconds. A known revocation ends a stream with Unauthenticated;
background synchronization failures do not close it. Signalman's idle Subscribe
handler observes cancellation without waiting for a message. During rollout,
upgrade Commodore's named snapshot and Signalman's event contract before the
consumers. Explicit `ValidateSession` RPC clients still require its additive
`session_state_checked` proof; downstream access checks no longer invoke it.

## Native credential ownership and compatibility

CLI storage uses `cli_session`, `cli_refresh` and `cli_bridge_url`; the tray
owns separate credentials. CLI renewal is serialized across processes and
stays bound to the original Bridge. `FW_USER_TOKEN` belongs to the invoking
process and is neither refreshed nor overwritten. CLI logout revokes the
stored CLI session, even when that invocation has a borrowed environment token.
Failures retain credentials for retry; `logout --local` explicitly clears only
local storage.

`auth status` authenticates with Bridge before displaying identity and session
expiry. `auth sessions list`, `revoke` and `revoke-others` use the shared GraphQL
operations and manage only the caller's sessions. Stored credentials use their
login Bridge; borrowed environment credentials use the active context without
reading storage or renewing. Requests refuse redirects. Own-session revocation
clears credentials under the renewal lock only if both the Bridge and stable
session ID still match, preserving a concurrent replacement login.

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

## Personal security activity

`securityActivityConnection` reads 90 days of sign-in, session-revocation,
password and wallet-link events through Periscope. Its RPC accepts pagination
only; tenant and user come from the interactive credential. API tokens, wallet
header delegations and service credentials cannot read this surface. Owner,
event-type and time predicates run before mirror deduplication in `api_events`.
The subject is the payload's `user_id`, independently of the audit actor, so
anonymous sign-in events and operator-initiated revocations remain attributed
to their affected user. The API exposes typed event metadata and omits raw
payloads, internal errors and token identifiers. Successful wallet sign-ins
record the verified wallet identity ID in the committed auth event. Auth events
store source IPs as IPv4 /24 or IPv6 /48 buckets, with invalid or absent addresses
left empty. Native sign-in events use the source metadata saved with the approved
grant rather than the later polling or code-redemption request.

## Security email

Interactive issuance records unfamiliar-device alerts in `account_email_outbox`
in the session transaction. Familiarity uses the original `(client_id,
device_label)` as a hashed key scoped to the tenant and user, retained for
90 days and touched by successful refreshes. Renaming the displayed session
label does not change that identity. Browser defaults describe browser and OS
without version numbers. Refresh-token theft queues a separate alert when the
affected session is revoked; `(kind, session ID)` deduplicates each obligation.
Neither alert has an opt-out.

Security rows use `security_pending`, which older verification-only workers
cannot claim or abandon. The upgraded worker leases both kinds with the same
fenced settlement and retry policy. Verification retains its 24-hour window;
security alerts retry through SMTP outages without that deadline. Missing SMTP
configuration leaves the alert pending. Delivery waits for a linked address to
be verified; wallet accounts without an address cannot receive email.

Alerts contain device, client, originating IP, UTC time and an Account → Security
link, with optional local MMDB location and provider attribution. They contain
no access or refresh credential. Missing GeoIP data does not block delivery.
History cleanup discovers at most 100 expired device keys and 100 terminal
email keys per sweep, then rechecks each owner's key. Familiarity expires after
2160 hours and terminal email metadata after 720 hours; pending alerts remain.

## Retention

Commodore runs bounded retention sweeps once per minute. Consumed or expired
authorization codes and expired device codes are removed after 24 hours.
Sessions are eligible 720 hours after revocation or idle expiry; the schema
keeps idle expiry at or before absolute expiry. Session refresh children are
locked and removed in batches of at most 100 per sweep before their parent is
deleted. A separate indexed pass removes at most 100 refresh rotations that
expired over 720 hours ago, including those belonging to still-live named
sessions. Every mutation includes the full tenant and user owner. Transactions
use the shared serialization/deadlock retry helper; rolled-back writes do not
consume the committed batch budget.

During mixed-version operation, a live refresh token without session ownership
preserves that owner's chain history. Cleanup checks for a live legacy successor
in a fresh READ COMMITTED statement after locking its candidate tokens. Locked
rows are skipped, and keyset cursors continue past protected owners before
wrapping to revisit them. This prevents cleanup from erasing the original-age
anchor of a chain being rotated by an older issuer. Revocation snapshots remain
available far longer than an access token can verify.

## Release state

The pending `v0.3.15` expand migration adds the session table, nullable refresh
ownership and authorization-code metadata. Existing-table indexes build online
and are validated after deployment. The service-owned
`commodore_auth_sessions_v0_3_15` data migration must finish before contract.
No session-ownership `NOT NULL` contract ships in the foundation migration.

Session management is self-service across the dashboard, CLI and GraphQL API.
The Security page, mandatory alerts, personal activity reader and native
approval details use the contracts described here. Team-member session
administration belongs to the separate team-role model.

## Dashboard controls

`/account/security` presents personal sessions, password controls, linked wallets
and 90-day security activity. Session, identity and activity reads have separate
loading/error state; an analytics failure does not hide session controls.
Session and wallet removals use confirmation dialogs. A failed mutation retains
its rows and credentials; only acknowledged current-session revocation clears
local state. The browser's existing logout path handles an explicitly selected
current session. An explicit `SESSION_REFRESH_REQUIRED` pre-execution refusal can
renew and retry once; other mutation errors are not automatically replayed.

Sign-in methods load from `GET /auth/me/wallets` through the existing interactive
JWT boundary, independently of cached profile snapshots. The owning service
filters by both user and tenant. Read failures remain errors instead of becoming
empty wallet lists. The server's owner lock and verified-password check still
protect final-wallet removal; the UI adds confirmation and the same conservative
availability hint. Settings links here instead of duplicating identity controls.

Device and PKCE consent show original request details, explicit expiry and the
warning to approve only a user-initiated sign-in. Device lookup responses are
bound to the submitted code so late responses cannot label a different code.
An invalid signed PKCE context never falls back to unverified URL metadata.
Legacy approval displays unknown provenance instead of inventing device details.

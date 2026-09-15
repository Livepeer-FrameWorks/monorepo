# RFC: Token Authority — Centralized Token Validation

## Status

Proposed. The Edge API path (Helmsman → Foghorn → Commodore) already follows the target delegation pattern via `ValidateAPIToken`; the RFC's Phase-1 unified `ValidateToken` RPC is not built, and the asymmetric-signing migration (Phase 2) has not started.

## TL;DR

- `JWT_SECRET` is shared across 10 services in dev compose. Any compromised service leaks the signing key.
- Commodore issues normal user/session JWTs. Skipper also mints a WebUI admin JWT
  from the same shared `JWT_SECRET`.
- Migrate to Commodore as the single token validation authority, then switch to asymmetric signing so the private key never leaves Commodore.
- Scope: this RFC covers the service/user JWT and developer API-token path only. Quartermaster's bootstrap/enrollment tokens and node fingerprints are a separate, already-shipped token system and are not a target of this centralization.

## Owning services / modules

Commodore (`api_control`) is the token issuer and the target validation authority. `pkg/auth` holds the mint/verify helpers and `pkg/middleware` the gRPC interceptor used by every service. Bridge (`api_gateway`) is the highest-volume verifier. Helmsman (`api_sidecar`) and Foghorn (`api_balancing`) form the shipped Edge API delegation chain. Skipper (`api_consultant`) is the one extra mint site (WebUI admin JWT). Quartermaster's bootstrap/enrollment-token authority is a separate module, out of scope here.

## Current State

### JWT tokens

Commodore is the primary user/session issuer. `auth.GenerateSessionJWT()` is called from four
`CommodoreServer` methods in `api_control/internal/grpc/server.go`:

- `Login` — password login
- `RefreshToken`
- `WalletLogin`
- `issueUserSessionTx` — the OAuth device-code and auth-code (PKCE) paths

Skipper also calls `auth.GenerateJWT()` for the WebUI admin flow, in
`api_consultant/cmd/skipper/main.go` (~line 872,
`auth.GenerateJWT("webui-admin", tenantID, "", "admin", jwtSecret)`). `GenerateJWT` is a thin
wrapper over `GenerateSessionJWT` with a nil role slice.

Session JWTs carry `user_id`, `tenant_id`, `email`, `role`, an RFC 9068 `roles` array, and
`auth_time`, with a 15-minute expiry (`auth.SessionTokenTTL`). The `roles` array is populated at
every Commodore mint site from `platformRoles(user.PlatformOperator)` and today carries the single
value `auth.RolePlatformOperator` (`platform_operator`). Gateway's delegated API-token assertion
(`auth.GenerateDelegatedAPITokenJWT`) adds `auth_type`, `token_id`, `permissions`, an audience, and
a `jti`, with a one-minute lifetime.

**Validation is distributed across two sites**, both using the same shared HMAC secret:

- Bridge — `auth.ValidateInteractiveJWT` in `api_gateway/internal/middleware/auth_request.go`
  (~line 110), with the Commodore API-token fallback immediately after (~line 126).
- The shared gRPC interceptors in `pkg/middleware/grpc.go` — `GRPCAuthInterceptor` (unary,
  `auth.ValidateJWT` at ~line 121) and `GRPCStreamAuthInterceptor` (stream, ~line 239). Every
  service that mounts them is a validator.

Skipper is not a validation site. It reads `JWT_SECRET` (`api_consultant/cmd/skipper/main.go`,
~line 78) only to mint the WebUI admin token and to pass the secret through to child config; it
calls no `auth.Validate*` helper.

`JWT_SECRET` is injected at 10 places in `docker-compose.yml`: Bridge, Foghorn, Quartermaster,
Signalman, Commodore, Periscope (query), Purser, Skipper, Deckhand, and the second-cell
`foghorn-b`.

### API tokens

Long-lived developer tokens, stored as SHA256 hashes in `commodore.api_tokens`. Validated
exclusively by Commodore's `ValidateAPIToken` RPC, declared at `pkg/proto/commodore.proto:146` and
implemented as `(*CommodoreServer).ValidateAPIToken` in `api_control/internal/grpc/server.go`
(~line 3029). Bridge calls it through `clients.Commodore.ValidateAPIToken` as a fallback when JWT
validation fails.

`ValidateAPIToken` is the only token-validation RPC Commodore exposes. There is no `ValidateToken`,
`GetPublicKey`, or `RevokeToken` RPC anywhere in `pkg/proto/`, and `pkg/auth` contains no Ed25519 or
JWKS code — every phase of the design below is unbuilt.

### SERVICE_TOKEN

Static shared secret for service-to-service gRPC calls. Every client in `pkg/clients/*/grpc_client.go`
sends it as a bearer token. Both interceptors in `pkg/middleware/grpc.go` compare it with
`subtle.ConstantTimeCompare` (~lines 104 and 225) and short-circuit before attempting JWT
validation.

### Edge API (already follows the target pattern)

The Edge API validates API tokens by delegating to the authority chain:

1. Helmsman receives bearer token from tray app / CLI
2. Sends `ValidateEdgeTokenRequest` to Foghorn over the control stream
3. Foghorn calls `CommodoreClient.ValidateAPIToken()`
4. Result cached in Helmsman with TTL

This works for API tokens today. JWTs are not yet supported because Foghorn's handler
only calls `ValidateAPIToken`, and that is the only validation RPC
`pkg/proto/commodore.proto` declares.

### Not in scope: bootstrap/enrollment tokens

Quartermaster mints and validates its own bootstrap/enrollment tokens (with node-fingerprint binding) for node enrollment. That is a separate, already-shipped token authority in its own trust domain — it never touches `JWT_SECRET` and is not part of the user/service JWT path this RFC centralizes. Nothing in this RFC changes it.

## Problem

1. **Shared secret blast radius.** `JWT_SECRET` is a symmetric HMAC key. If any of the services holding it is compromised, the attacker can forge JWTs for any user, including one carrying `platform_operator` in `roles`.

2. **No rotation without downtime.** Changing the secret requires restarting all services simultaneously. There's no dual-key transition mechanism.

3. **No revocation.** A valid JWT is accepted by every service until it expires. There's no way to invalidate a specific token (e.g., on logout, password change, or account suspension).

4. **SERVICE_TOKEN is worse.** It's a static string that never expires, shared by all services, and grants full service-level access.

## Goals

- Commodore becomes the single authority for token validation.
- Remove `JWT_SECRET` from all services except Commodore (Phase 2).
- Support key rotation without downtime.
- Keep Gateway performance acceptable (it handles the most auth traffic).

## Non-Goals

- Replace SERVICE_TOKEN in this RFC (separate concern — service identity / mTLS).
- Quartermaster's bootstrap/enrollment-token and node-fingerprint authority (separate, shipped system — see Current State).
- Implement fine-grained RBAC (separate RFC).
- Change the JWT claims structure. The claim set is not frozen at what this RFC was first written
  against: the RFC 9068 `roles` array (carrying `platform_operator`) and `auth_time` already ship on
  every session token, and delegated API-token assertions already carry `auth_type`, `token_id`, and
  `permissions`. The non-goal means the migration must carry the existing claims through unchanged,
  not that the claim set is minimal. In particular a `ValidateToken` response and any Ed25519-signed
  token must preserve `roles`, or every platform-operator authorization check
  (`Claims.HasRole(auth.RolePlatformOperator)`) silently fails closed.

## Proposed Design

### Phase 1: Unified ValidateToken RPC

Add a `ValidateToken` RPC to Commodore that accepts any bearer token and returns a uniform result:

```proto
// commodore.proto
rpc ValidateToken(ValidateTokenRequest) returns (ValidateTokenResponse);

message ValidateTokenRequest {
  string token = 1;
}

message ValidateTokenResponse {
  bool valid = 1;
  string auth_type = 2;           // "jwt", "api_token"
  string user_id = 3;
  string tenant_id = 4;
  string email = 5;
  string role = 6;                // Tenant role
  repeated string permissions = 7;
  string token_id = 8;            // For API tokens
  repeated string roles = 9;      // RFC 9068 roles claim, e.g. platform_operator
  int64 auth_time = 10;
}
```

Commodore detects the token type internally:

- Starts with `eyJ` → JWT → validate with local secret, extract claims
- Otherwise → API token → DB lookup (existing `ValidateAPIToken` path)

**Consumers:**

- Foghorn's `processValidateEdgeToken` switches from `ValidateAPIToken` to `ValidateToken` (supports JWTs for tray app login)
- Any new service that needs user context calls `ValidateToken` instead of local JWT validation

**Gateway stays on local validation** — it processes thousands of requests/sec. Adding a Commodore round-trip per request is not acceptable. Phase 2 solves this properly.

### Phase 2: Asymmetric Signing (Ed25519)

Replace HMAC-SHA256 with Ed25519 signing:

1. Commodore generates an Ed25519 key pair on first start, stores the private key in its database (or Vault when available).
2. Commodore signs JWTs with the private key using `EdDSA` algorithm.
3. Commodore exposes the public key via:
   - `GetPublicKey` gRPC RPC (for internal services)
   - `/.well-known/jwks.json` HTTP endpoint (for external consumers)
4. Services fetch the public key at startup and on a refresh interval. They validate JWTs locally — no shared secret needed.
5. `JWT_SECRET` is removed from all services except during the migration window.

**Key rotation:**

- Commodore generates a new key pair, starts signing with the new key.
- Both old and new public keys are published in the JWKS response.
- After `max_jwt_lifetime` (15 min), the old key is removed.
- Services that cache the public key refresh it periodically (e.g., every 5 min).

**Migration path:**

1. Commodore starts signing with Ed25519 but also accepts HMAC-SHA256 JWTs (dual validation).
2. Roll out new Commodore. All new JWTs are Ed25519-signed.
3. After 15 minutes, all active JWTs are Ed25519. Remove HMAC fallback.
4. Remove `JWT_SECRET` from other services' env configs.

### Phase 3: Token Revocation

Add a lightweight revocation mechanism:

1. Commodore maintains a revocation set in Redis, keyed by JWT `jti` (JWT ID) claim. TTL = JWT max lifetime (15 min).
2. `RevokeToken` RPC added to Commodore. Called on logout, password change, account suspension.
3. `ValidateToken` RPC checks revocation before returning `valid=true`.
4. Services doing local Ed25519 validation can optionally check revocation for sensitive operations via `IsTokenRevoked` RPC.

The short JWT lifetime (15 min) limits the revocation window. Most use cases (logout, password change) are adequately served by not refreshing the token.

## Security Considerations

- **Phase 1 does not reduce the attack surface** — `JWT_SECRET` is still shared. It only adds a centralized validation path for services that don't need local validation.
- **Phase 2 eliminates the shared secret.** The private key exists only in Commodore's memory/storage. Compromising any other service does not allow JWT forgery.
- **Ed25519 over RSA** — faster signing/verification, smaller keys (32 bytes), simpler implementation. No padding oracle attacks.
- **JWKS endpoint must be internal-only** — not exposed to the public internet. Services fetch it over the internal network / mesh.

## Open Questions

1. **Should Gateway switch to Ed25519 local validation or Commodore RPC?** Ed25519 local validation is the intended path — same performance as today, no shared secret.
2. **Where should the Ed25519 private key be stored?** Database (encrypted at rest) is simplest. Vault integration is better but adds a dependency.
3. **Should SERVICE_TOKEN be addressed here?** Probably not — service identity is a separate concern better solved by mTLS. Transport TLS is documented in `docs/architecture/tls.md`; mTLS is unbuilt (`pkg/grpcutil/tls.go` never sets `ClientAuth`). SERVICE_TOKEN could instead be replaced by service-specific Ed25519 tokens issued by Commodore.
4. **Timeline.** Phase 1 is low-effort (one RPC). Phase 2 is medium-effort but high-impact. Phase 3 is optional until compliance requires it.

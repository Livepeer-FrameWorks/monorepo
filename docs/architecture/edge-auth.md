# Edge-to-Foghorn Authentication

Edge nodes authenticate to Foghorn over the public internet using three layers:
transport encryption via TLS, identity enrollment through Quartermaster-validated
tokens, and service-to-service authorization via a shared static token.

## Transport: Split gRPC Listeners

Foghorn has multiple legitimate inbound surfaces:

- Public HTTP redirect/source endpoints used by viewers and MistServer.
- Public gRPC endpoints used by Helmsman and self-hosted edge bootstrap.
- Internal logical-cluster gRPC endpoints used by Commodore, Quartermaster
  health polling, and Foghorn peers.

Those callers do not all share a trust boundary, so Foghorn uses two gRPC
listeners in one process:

| Listener | Default bind | Certificate                                                                 | Names                                  | Audience                                                                   |
| -------- | ------------ | --------------------------------------------------------------------------- | -------------------------------------- | -------------------------------------------------------------------------- |
| Internal | `:18019`     | File-based internal-CA leaf from `GRPC_TLS_CERT_PATH` / `GRPC_TLS_KEY_PATH` | `foghorn.internal` and mesh names      | Commodore control RPCs, Quartermaster health polling, federation, HA relay |
| External | `:18029`     | Navigator ACME cluster wildcard (`cluster:{cluster_slug}`)                  | `foghorn.{cluster_slug}.{root_domain}` | Helmsman control and edge bootstrap/enrollment                             |

The external listener serves only Navigator-backed cluster TLS bundles. If
Foghorn is configured with Navigator in production but cannot load a cluster
bundle for its served clusters, startup fails instead of running a process that
Helmsman and edge bootstrap cannot trust.

Federation belongs on the internal listener. The peer manager reads
`TenantClusterPeer.foghorn_grpc_addr` from Quartermaster and expects an internal
mesh address with the `foghorn.internal` identity. Missing peer addresses should
fail discovery instead of falling back to the public edge-bootstrap listener.

Insecure fallback is local-dev only, when neither internal file TLS nor
Navigator TLS is configured.

Client-side TLS is explicit. Edge provisioning only writes `GRPC_TLS_CA_PATH`
when Helmsman is configured to dial an internal-CA address such as
`foghorn.internal:18019`. Managed edge nodes normally dial the external listener
at `foghorn.{cluster_slug}.{root_domain}:18029` and validate the public ACME
certificate with system roots.

Bridge is allowed to proxy only the public edge-bootstrap `PreRegisterEdge` RPC
after Quartermaster validates the bootstrap token and returns a public Foghorn
address. Bridge does not own tenant/media control routing; those calls go
through Commodore and the internal Foghorn listener.

**Implementation:** `api_balancing/internal/control` (server),
`api_sidecar/internal/control` (client)

## Identity: Two-Phase Edge Enrollment

### Phase 1: Fingerprint Resolution (Returning Nodes)

When an edge reconnects, Foghorn calls `quartermasterClient.ResolveNodeFingerprint`
with:

- Peer IP (from `x-forwarded-for` metadata or gRPC peer address)
- Local IPv4/IPv6 addresses (reported by Helmsman)
- MAC address SHA256 hash
- Machine ID SHA256 hash
- GeoIP data (country, city, latitude, longitude)

If Quartermaster finds a matching fingerprint, it returns the canonical `node_id`
and `tenant_id`. The edge is registered immediately.

**Implementation:** `api_balancing/internal/control`

### Phase 2: Token-Based Enrollment (New Nodes)

If fingerprint resolution fails, the edge must provide an enrollment token.
Foghorn calls `quartermasterClient.BootstrapEdgeNode` with the token and
fingerprint data. Quartermaster validates and binds the fingerprint to a new
`node_id`.

Token payload:

- `tenant_id` — owning organization
- `cluster_id` — target cluster (optional; defaults to Foghorn's `CLUSTER_ID`)
- `kind` — must be `"edge_node"`
- `usage_limit` — how many nodes can consume this token (for bulk provisioning)

Tokens are created through the dashboard/GraphQL and MCP cluster flows
(`createEdgeCluster`, `createEnrollmentToken`, `create_edge_cluster`,
`create_enrollment_token`), by `frameworks edge deploy` via Bridge, or by
admin CLI commands such as `frameworks admin clusters create-edge`,
`frameworks admin clusters enrollment-token`, and
`frameworks admin bootstrap-tokens create --kind edge_node`.
They are consumed atomically during `BootstrapEdgeNode` unless `usage_limit > 1`.

**Implementation:** `api_balancing/internal/control`

### Pre-Flight Registration (PreRegisterEdge)

Before Helmsman connects, the CLI can call `EdgeProvisioningService.PreRegisterEdge`
to validate the enrollment token and receive node assignment data:

1. Validates token via `ValidateBootstrapTokenEx` (with IP binding, without consumption)
2. Uses the preferred human-readable node ID when provided and valid, otherwise generates a 6-byte hex fallback
3. Constructs edge FQDN: `{node_label}.{cluster_slug}.{root_domain}` where `node_label` is the node ID with a single `edge-` prefix
4. Constructs pool FQDN: `edge.{cluster_slug}.{root_domain}`
5. Returns the internal CA bundle for gRPC trust bootstrap

The CLI uses this to stage certs and config before starting Caddy/Helmsman.

**Implementation:** `api_balancing/internal/control`

## Authorization: SERVICE_TOKEN Interceptor

gRPC method invocations are authorized by `middleware.GRPCAuthInterceptor`. The
interceptor checks `Authorization: Bearer <token>` against:

1. **SERVICE_TOKEN** — constant-time comparison for service-to-service calls
2. **JWT** — signed user tokens with `tenant_id`, `user_id`, `role` claims

### Method Exemptions

| Method                             | Auth mechanism                       |
| ---------------------------------- | ------------------------------------ |
| `HelmsmanControl.Connect`          | Enrollment token validated in-method |
| `EdgeProvisioning.PreRegisterEdge` | Enrollment token validated in-method |
| `Health.Check`, `Health.Watch`     | No auth required                     |

Streaming RPCs are similarly protected, except `HelmsmanControl.Connect` which
validates enrollment tokens per-message.

`FoghornFederation.PeerChannel` is **not** exempt — it uses SERVICE_TOKEN for
cluster-to-cluster communication.

**Implementation:** `api_balancing/internal/control` (exemptions),
`pkg/middleware` (interceptor)

## Operator-only Mist Admin Surface

The MistServer controller (port `4242`) is bound to loopback on every edge and is **never** publicly reachable. Mist admin access lets the caller reconfigure protocols and triggers, walk the local filesystem, and launch processes — equivalent to a shell on the box — so the auth model is **node ownership, enforced at two walls**, not a single permission string.

### Wire

```
operator browser
  │ 1. openMistAdminSession(input: {nodeId}) GraphQL mutation
  ▼
api_gateway resolver (DoOpenMistAdminSession)
  │ ← first wall: Quartermaster.GetNodeOwner + GetCluster
  │   policy: caller role in {owner, admin} AND
  │           caller tenant == cluster.owner_tenant_id;
  │           system tenant owner/admin is platform break-glass
  │ 2. Commodore.MintMistAdminSession(node_id)
  ▼
api_control (Commodore)
  │ ← second wall: same policy on trusted gRPC metadata identity
  │ 3. signs JWT { purpose: edge_mist_admin, node_id, cluster_id,
  │              tenant_id, user_id, role, jti, exp ~5min }
  │ returns { token, expires_at, edge_domain }
  ▼
resolver returns MistAdminSession { postUrl, sessionToken, expiresAt }
  │
  │ 4. webapp hidden POST form (target=_blank)
  │    POST https://{edge_domain}/_mist-session
  │    body: session_token=<jwt>
  ▼
edge Caddy → handle @mist_admin (host-matched to edge FQDN,
  │         /_mist-session /_mist /_mist/*)
  │ 5. reverse_proxy → helmsman:18007 (preserves path)
  ▼
api_sidecar (Helmsman)
  │ /_mist-session POST handler:
  │   - control.ValidateMistAdminSession(token) → Foghorn over bidi gRPC stream
  │   - Foghorn relays to Commodore.ValidateMistAdminSession injecting the
  │     CONNECTED node's nodeID as expected_node_id (so a token minted for
  │     one node fails on every other)
  │   - on valid: sets fw_mist_admin cookie (HttpOnly, Secure, SameSite=Lax,
  │     Path=/_mist, Max-Age aligned with JWT exp), 302 → /_mist/
  │
  │ /_mist/* subsequent requests:
  │   - RequireMistAdmin reads fw_mist_admin cookie
  │   - cookie → ValidateMistAdminSession path
  │   - on auth: reverse-proxy to 127.0.0.1:4242 (Mist controller)
  ▼
MistServer controller LSP UI
```

### Critical invariants

1. **`MIST_API_PASSWORD` never leaves the box.** Native edges rely on Mist's loopback auto-auth — the reverse-proxy director scrubs `Forwarded` / `X-Forwarded-For` / `X-Forwarded-Host` / `X-Forwarded-Proto` / `X-Real-IP` so Mist sees a pure loopback caller and skips Basic Auth. Docker edges (`mistserver:4242` not loopback) are unsupported and respond `501` because they cannot rely on loopback auto-auth.
2. **Cookie is path-scoped to `/_mist`** so it can't be sent on `/view/*` or any other origin path; never `Path=/`.
3. **Authorization / Cookie scrubbed on the upstream** request so the operator's platform JWT and session never reach Mist.
4. **Set-Cookie scrubbed on the downstream** response so Mist's controller session doesn't collide with platform cookies on the same eTLD+1.
5. **The session token is bound to a single `node_id`** in its JWT claims — Foghorn always injects the connected Helmsman's nodeID as `expected_node_id`, so replay against any other edge fails inside Commodore.
6. **Developer API tokens are not accepted by the proxy.** API-token validation is not node-bound, so the only way into `/_mist/*` is a Mist-admin session minted by the GraphQL ownership flow.
7. **Minting is audited** through the `mist_admin_session_minted` service event; the event records user, tenant, node, and cluster metadata, never the session token.
8. **Caddy uses `handle`, not `handle_path`** for `/_mist` so the prefix survives to Helmsman, which is the only place that strips it. The LSP frontend derives its API base from `location.pathname`, so a mid-route strip breaks the UI's relative paths.
9. **The admin matcher is host-matched** (`host {edge_domain}`) inside the shared Caddy snippet — never bare-path — so tenant/customer hosts that import the same snippet cannot inherit the admin surface.

### Files

| File                                                                       | Purpose                                                                       |
| -------------------------------------------------------------------------- | ----------------------------------------------------------------------------- |
| `pkg/auth/mist_admin_session.go`                                           | JWT mint/validate primitives with node-binding enforcement                    |
| `pkg/proto/commodore.proto`                                                | `Mint`/`ValidateMistAdminSession` RPCs                                        |
| `pkg/proto/ipc.proto`                                                      | `EdgeMistAdminSession{Request,Response}` over the Helmsman control stream     |
| `api_control/internal/grpc/server.go` (`MintMistAdminSession`)             | Second-wall ownership enforcement; trusted-context identity                   |
| `api_balancing/internal/control/server.go` (`processEdgeMistAdminSession`) | Relay; injects connected node's `expected_node_id`                            |
| `api_sidecar/internal/handlers/mist_admin_proxy.go`                        | Reverse proxy + `RequireMistAdmin` + `/_mist-session`                         |
| `api_sidecar/internal/config/caddyfile.go`                                 | Host-matched `@mist_admin` matcher in the production Caddy snippet            |
| `api_gateway/internal/resolvers/mist_admin_session.go`                     | First-wall resolver; mirrors the Commodore policy via `mistAdminCanAdminNode` |
| `website_application/src/lib/components/nodes/OpenMistAdminButton.svelte`  | Hidden-POST-form bridge to `/_mist-session`                                   |

## Key Files

| File                             | Purpose                                                                 |
| -------------------------------- | ----------------------------------------------------------------------- |
| `api_balancing/internal/control` | TLS setup, Connect handler, PreRegisterEdge, auth exemptions            |
| `api_sidecar/internal/control`   | Client-side TLS (FQDN auto-detect)                                      |
| `pkg/middleware`                 | SERVICE_TOKEN interceptor                                               |
| `pkg/proto`                      | `PreRegisterEdgeRequest`, `EdgeFingerprint` definitions                 |
| `pkg/proto`                      | `BootstrapEdgeNode`, `ValidateBootstrapToken`, `ResolveNodeFingerprint` |

## Forward: mTLS

The current model uses server-only TLS. Foghorn validates edge identity through
enrollment tokens and fingerprints, but edges do not present client certificates.

Future work: per-edge client certificates issued during enrollment, Foghorn
verifies `CN=node_id`. See `docs/rfcs/service-identity-and-cluster-binding.md`
for the conceptual proposal (not yet implemented).

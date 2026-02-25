# Edge-to-Foghorn Authentication

Edge nodes authenticate to Foghorn over the public internet using three layers:
transport encryption via TLS, identity enrollment through Quartermaster-validated
tokens, and service-to-service authorization via a shared static token.

## Transport: TLS with Navigator-Backed Wildcard Cert

Foghorn auto-detects TLS configuration using three fallback sources:

1. **File-based certificates** (`GRPC_TLS_CERT_PATH`, `GRPC_TLS_KEY_PATH`)
2. **Navigator wildcard certificate** for `*.{cluster_slug}.{root_domain}` with
   hot-reload via `tls.Config.GetCertificate` and `atomic.Value` storage
3. **Insecure fallback** (local dev only)

The server checks for file-based env vars first, then queries Navigator if
available. Navigator returns the cluster wildcard cert which Foghorn stores
atomically and rotates at runtime without restart.

Client-side TLS is auto-detected in Helmsman: if the Foghorn address contains a
dot (FQDN), TLS is enabled. Docker service names without dots default to insecure
connections for local development.

`GRPC_USE_TLS` is **not used**. TLS is determined by cert availability (server)
and FQDN detection (client).

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

Tokens are created via the admin CLI (`skipper edge token create`) or the web UI.
They are consumed atomically on use unless `usage_limit > 1`.

**Implementation:** `api_balancing/internal/control`

### Pre-Flight Registration (PreRegisterEdge)

Before Helmsman connects, the CLI can call `EdgeProvisioningService.PreRegisterEdge`
to validate the enrollment token and receive node assignment data:

1. Validates token via `ValidateBootstrapTokenEx` (with IP binding and consumption)
2. Generates a 6-byte hex `node_id`
3. Constructs edge FQDN: `edge-{node_id}.{cluster_slug}.{root_domain}`
4. Constructs pool FQDN: `edge.{cluster_slug}.{root_domain}`
5. Retrieves cluster wildcard TLS certificate from Navigator and returns it inline

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

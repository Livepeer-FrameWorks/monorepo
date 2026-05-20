# Edge-to-Foghorn Authentication

Edge nodes authenticate to Foghorn over the public internet using three layers:
transport encryption via TLS, identity enrollment through Quartermaster-validated
tokens, and service-to-service authorization via a shared static token.

## Transport: SNI-Routed TLS for Internal and Cluster Names

Foghorn has multiple legitimate inbound surfaces:

- Public HTTP redirect/source endpoints used by viewers and MistServer.
- Public gRPC endpoints used by Helmsman and self-hosted edge bootstrap.
- Logical-cluster gRPC endpoints used by Quartermaster health polling.
- Peer/federation gRPC endpoints between physical Foghorn instances.

Those callers do not all use the same DNS name, so Foghorn must not choose one
certificate family globally. In production it loads both:

1. The **file-based internal service leaf** (`GRPC_TLS_CERT_PATH`,
   `GRPC_TLS_KEY_PATH`) for mesh/internal names such as `foghorn.internal`.
2. The **Navigator cluster TLS bundle** (`cluster:{cluster_slug}`) for
   `{cluster_slug}.{root_domain}` and `*.{cluster_slug}.{root_domain}`. This
   covers names such as `foghorn.media-eu-1.frameworks.network` and
   `edge-eu-1.media-eu-1.frameworks.network`.

The gRPC listener serves these bundles together through `tls.Config.GetCertificate`
and selects by SNI. Exact SAN/name matches win, wildcard cluster matches are next,
and the internal service leaf is the default. Bundle refreshes replace the whole
served set atomically. If Foghorn is configured with Navigator in production but
cannot load a cluster bundle for its served clusters, startup fails instead of
running a process that Quartermaster and edge bootstrap cannot trust.

Insecure fallback is local-dev only, when neither file TLS nor Navigator TLS is
configured.

Client-side TLS is auto-detected in Helmsman. TLS is used whenever the Foghorn
address is an FQDN, trust material is provided via `GRPC_TLS_*`, or
`GRPC_ALLOW_INSECURE=false`. Docker service names without dots can still use
insecure connections for local development when explicitly allowed.

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

# Edge-to-Foghorn Authentication

Edge nodes authenticate to Foghorn over the public internet using three layers:
transport encryption via TLS, identity enrollment through Quartermaster-validated
tokens, and service-to-service authorization via a shared static token.

## Transport: Split gRPC Listeners

Foghorn has multiple legitimate inbound surfaces:

- Public HTTP redirect/source endpoints used by viewers and MistServer.
- Public gRPC endpoints used by Helmsman and self-hosted edge bootstrap.
- Logical-cluster gRPC endpoints used by Quartermaster health polling.
- Peer/federation gRPC endpoints between physical Foghorn instances.

Those callers do not all share a trust boundary, so Foghorn uses two gRPC
listeners in one process:

| Listener | Default bind | Certificate                                                                 | Names                                  | Audience                                                                           |
| -------- | ------------ | --------------------------------------------------------------------------- | -------------------------------------- | ---------------------------------------------------------------------------------- |
| Internal | `:18019`     | File-based internal-CA leaf from `GRPC_TLS_CERT_PATH` / `GRPC_TLS_KEY_PATH` | `foghorn.internal` and mesh names      | Foghorn HA relay                                                                   |
| External | `:18029`     | Navigator ACME cluster wildcard (`cluster:{cluster_slug}`)                  | `foghorn.{cluster_slug}.{root_domain}` | Helmsman control, edge bootstrap/enrollment, Quartermaster polling, and federation |

The external listener serves only Navigator-backed cluster TLS bundles. If
Foghorn is configured with Navigator in production but cannot load a cluster
bundle for its served clusters, startup fails instead of running a process that
Quartermaster, federation peers, and edge bootstrap cannot trust.

Federation belongs on the external listener. The peer manager reads
`TenantClusterPeer.foghorn_grpc_addr` from Quartermaster and, when absent, falls
back to `foghorn.{cluster_slug}.{base_url}:18029` in
`api_balancing/internal/federation/peer_manager.go`. That address is a cluster
FQDN backed by the public ACME wildcard, not an internal mesh identity.

Insecure fallback is local-dev only, when neither internal file TLS nor
Navigator TLS is configured.

Client-side TLS is explicit. Edge provisioning only writes `GRPC_TLS_CA_PATH`
when Helmsman is configured to dial an internal-CA address such as
`foghorn.internal:18019`. Managed edge nodes normally dial the external listener
at `foghorn.{cluster_slug}.{root_domain}:18029` and validate the public ACME
certificate with system roots.

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

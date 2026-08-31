# TLS Architecture

FrameWorks uses two certificate systems:

| System      | Issuer                    | Trust anchor                    | Distribution                                                                                          | Primary use                                       |
| ----------- | ------------------------- | ------------------------------- | ----------------------------------------------------------------------------------------------------- | ------------------------------------------------- |
| Internal CA | Navigator internal CA     | FrameWorks root/intermediate CA | Privateer writes `/etc/frameworks/pki/ca.crt` and `/etc/frameworks/pki/services/<service>/{cert,key}` | Service-to-service gRPC on the private network    |
| Public ACME | Navigator via lego DNS-01 | Web PKI                         | Navigator stores ACME bundles and publishes them to Caddy or Foghorn                                  | Public HTTPS and Foghorn's external gRPC listener |

There is no separate "mesh TLS" tier. Mesh traffic uses the internal CA. Public
traffic uses ACME. Authentication is token-over-TLS (`SERVICE_TOKEN`, JWT, or
enrollment tokens), not transport mTLS. The WireGuard substrate that carries this
traffic is documented in `privateer-mesh.md`.

## Internal Certificate Distribution

Privateer uses two distinct credentials during internal certificate renewal:

- `SERVICE_TOKEN` authenticates Privateer's gRPC transports to Quartermaster and
  Navigator.
- A node- and cluster-bound `cert_sync` bootstrap token authorizes Navigator to
  issue a particular node's service leaves. Privateer mints this credential from
  Quartermaster and sends it in each `IssueInternalCert` request.

Internal service leaves are valid for 72 hours and Privateer renews them inside
the final 36 hours. Runtime-minted issuance tokens are valid for 30 days;
Privateer refreshes them with seven days remaining. If Navigator rejects an
issuance token (including an operator-provided token whose expiry is unknown),
Privateer mints a replacement and retries that issuance once immediately. A
failed proactive refresh does not discard a token that is still valid.

Three consecutive certificate-sync failures make Privateer's `internal_pki`
health check unhealthy and return HTTP 503 from its health endpoint. The
`privateer_internal_cert_sync_operations_total{status}` and
`privateer_internal_cert_issue_token_refreshes_total{reason}` metrics expose the
same lifecycle. `frameworks cluster doctor` probes Privateer on every effective
backend host, and cluster snapshots report each leaf's validity window and
certificate/key match state.

## Listener Policy

All gRPC services except Foghorn serve a single internal-CA listener. Their
certificate ServerName is the logical service name, for example
`decklog.internal` or `quartermaster.internal`.

Foghorn is the exception because it is both an internal control-plane service and
the public edge authority:

| Listener | Default bind | Certificate           | ServerName                 | Audience                                                                 |
| -------- | ------------ | --------------------- | -------------------------- | ------------------------------------------------------------------------ |
| Internal | `:18019`     | Internal CA           | `foghorn.internal`         | Commodore control RPCs, Foghorn federation, HA relay, and gRPC health    |
| External | `:18029`     | ACME cluster wildcard | `foghorn.<cluster>.<root>` | Helmsman control streams and the narrow edge bootstrap `PreRegisterEdge` |

The external listener must not serve the internal leaf. If Navigator cannot
provide a cluster wildcard bundle in production, Foghorn fails startup.

Public HTTP remains a Caddy concern unless a service explicitly owns an HTTP
endpoint. Foghorn's viewer redirect and MistServer pull-routing surfaces are
separate from the gRPC split.

## Client Authority

gRPC clients construct an explicit dial tuple: address, ServerName, CA material,
and insecure policy. The address is the network route; the ServerName is the TLS
identity. These are not interchangeable.

When a custom CA path or inline CA PEM is configured, `pkg/grpcutil.ClientTLS`
requires a non-empty ServerName and fails closed otherwise. Client packages in
`pkg/clients/` export canonical service names but do not read environment
variables. Entrypoints read environment and pass fully specified client config.

`GRPC_TLS_SERVER_NAME` is not a process-wide runtime knob. Multi-client services
such as Bridge must pass one ServerName per downstream client.

Bridge is not a general Foghorn client. Its only permitted direct Foghorn RPC is
the public edge-bootstrap `PreRegisterEdge` rendezvous after Quartermaster has
validated the bootstrap token and returned a public Foghorn address. Tenant and
media control flows stay on Commodore, which resolves the correct internal
Foghorn through Quartermaster routing.

## Environment Knobs

| Variable                                                      | Scope                                                          |
| ------------------------------------------------------------- | -------------------------------------------------------------- |
| `GRPC_TLS_CERT_PATH`, `GRPC_TLS_KEY_PATH`, `GRPC_TLS_CA_PATH` | Internal gRPC TLS files                                        |
| `GRPC_ALLOW_INSECURE`                                         | Internal gRPC dev/test escape hatch only                       |
| `<SERVICE>_GRPC_TLS_SERVER_NAME`                              | Per-client override at service entrypoints                     |
| `FOGHORN_INTERNAL_GRPC_BIND_ADDR`                             | Foghorn internal listener, default `:18019`                    |
| `FOGHORN_EXTERNAL_GRPC_BIND_ADDR`                             | Foghorn external listener, default `:18029`                    |
| `FOGHORN_EXTERNAL_GRPC_PORT`                                  | Public edge listener port returned for edge bootstrap          |
| `FOGHORN_RELAY_ADVERTISE_ADDR`                                | Internal HA relay address stored in Redis connection ownership |
| `FRAMEWORKS_BOOTSTRAP_INSECURE`                               | Privateer HTTPS bootstrap dev/test escape hatch only           |

## Open Items

- Intermediate CA rotation is tracked in `docs/rfcs/internal-ca-intermediate-rotation.md`.
- SPIFFE-style workload identity is not implemented. It is a larger security
  model change, not required for the current token-over-TLS deployment.

## Restart-safe edge configuration

Foghorn persists the last complete Helmsman `ConfigSeed`, including tenant/site
configuration and the complete TLS bundle set. A restart with Quartermaster or
Navigator unavailable preserves only fields whose authority could not be
resolved; an authoritative empty response removes stale state, and a partial
TLS refresh cannot replace a complete last-good set. Helmsman persists and
reapplies its last-good configuration locally, so an already provisioned media
node does not require a control-plane read merely to restart. Certificate hard
expiry remains the honest availability boundary; persistence never extends a
certificate's validity.

The Ed25519 media-authority trust set and X25519 cell seal keys are separate
from both TLS systems above. Their production render/custody rules are described
in [Media-cluster authority](media-authority.md).

# TLS Architecture

FrameWorks uses two certificate systems:

| System      | Issuer                    | Trust anchor                    | Distribution                                                                                          | Primary use                                       |
| ----------- | ------------------------- | ------------------------------- | ----------------------------------------------------------------------------------------------------- | ------------------------------------------------- |
| Internal CA | Navigator internal CA     | FrameWorks root/intermediate CA | Privateer writes `/etc/frameworks/pki/ca.crt` and `/etc/frameworks/pki/services/<service>/{cert,key}` | Service-to-service gRPC on the private network    |
| Public ACME | Navigator via lego DNS-01 | Web PKI                         | Navigator stores ACME bundles and publishes them to Caddy or Foghorn                                  | Public HTTPS and Foghorn's external gRPC listener |

There is no separate "mesh TLS" tier. Mesh traffic uses the internal CA. Public
traffic uses ACME. Authentication is token-over-TLS (`SERVICE_TOKEN`, JWT, or
enrollment tokens), not transport mTLS.

## Listener Policy

All gRPC services except Foghorn serve a single internal-CA listener. Their
certificate ServerName is the logical service name, for example
`decklog.internal` or `quartermaster.internal`.

Foghorn is the exception because it is both an internal control-plane service and
the public edge/federation authority:

| Listener | Default bind | Certificate           | ServerName                 | Audience                                                                           |
| -------- | ------------ | --------------------- | -------------------------- | ---------------------------------------------------------------------------------- |
| Internal | `:18019`     | Internal CA           | `foghorn.internal`         | Foghorn HA relay                                                                   |
| External | `:18029`     | ACME cluster wildcard | `foghorn.<cluster>.<root>` | Helmsman, edge bootstrap/enrollment, Quartermaster polling, and Foghorn federation |

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

## Environment Knobs

| Variable                                                      | Scope                                                           |
| ------------------------------------------------------------- | --------------------------------------------------------------- |
| `GRPC_TLS_CERT_PATH`, `GRPC_TLS_KEY_PATH`, `GRPC_TLS_CA_PATH` | Internal gRPC TLS files                                         |
| `GRPC_ALLOW_INSECURE`                                         | Internal gRPC dev/test escape hatch only                        |
| `<SERVICE>_GRPC_TLS_SERVER_NAME`                              | Per-client override at service entrypoints                      |
| `FOGHORN_INTERNAL_GRPC_BIND_ADDR`                             | Foghorn internal listener, default `:18019`                     |
| `FOGHORN_EXTERNAL_GRPC_BIND_ADDR`                             | Foghorn external listener, default `:18029`                     |
| `FOGHORN_EXTERNAL_GRPC_PORT`                                  | Port advertised to Quartermaster for external Foghorn consumers |
| `FOGHORN_RELAY_ADVERTISE_ADDR`                                | Internal HA relay address stored in Redis connection ownership  |
| `FRAMEWORKS_BOOTSTRAP_INSECURE`                               | Privateer HTTPS bootstrap dev/test escape hatch only            |

## Open Items

- Intermediate CA rotation is tracked in `docs/rfcs/internal-ca-intermediate-rotation.md`.
- SPIFFE-style workload identity is not implemented. It is a larger security
  model change, not required for the current token-over-TLS deployment.

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

Each leaf directory `/etc/frameworks/pki/services/<service>/` has two writers:
Privateer at runtime and the `go_service` Ansible role when it installs a
bootstrap pair. Both take an exclusive `flock` on the directory itself before
swapping `tls.crt` and `tls.key`, so a reader never sees a cert from one writer
with a key from the other, and no lock file is left in the PKI tree.

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

| Listener | Provisioned bind              | Certificate           | ServerName                 | Audience                                                                 |
| -------- | ----------------------------- | --------------------- | -------------------------- | ------------------------------------------------------------------------ |
| Internal | loopback + WireGuard `:18019` | Internal CA           | `foghorn.internal`         | Commodore control RPCs, Foghorn federation, HA relay, and gRPC health    |
| External | every interface `:18029`      | ACME cluster wildcard | `foghorn.<cluster>.<root>` | Helmsman control streams and the narrow edge bootstrap `PreRegisterEdge` |

The external listener must not serve the internal leaf. If Navigator cannot
provide a cluster wildcard bundle in production, Foghorn fails startup.

The network observer is an internal HTTP service rather than a gRPC one. It serves
its API on an internal-CA HTTPS listener (`:18034`, ServerName
`network-observer.internal`), and its plain `:18024` listener then answers only health
and readiness probes. Bridge dials it at its aggregator-region mesh alias and verifies
that ServerName, as for gRPC clients. The observer has no public certificate; external
probers reach it through Bridge.

A service deployed in Docker mode reads the same paths: the `compose_stack` role
bootstraps the CA bundle and leaf as `go_service` does, mounts their directories
(`/etc/frameworks/pki`) read-only at the same path so Privateer's renewals reach the
container, and adds the container to the host's `frameworks` group, which may read the
leaf key.

### Listener binds

Every listener on a platform host binds loopback and the host's WireGuard address, or
loopback alone. Its callers are on loopback (the host's native reverse proxy, the local
vmagent, the rollout gate, the CLI's SSH sessions) or on the mesh (a docker-mode proxy,
which reaches local services on the WireGuard address rather than the Docker gateway, the
peers' proxy backups, gRPC and database clients at `<host>.internal` or
`<service>.internal`, replication and Raft peers, and Quartermaster's health probes).
A listener on the LAN or public interface would bypass the reverse proxy's TLS and
client-IP headers, and the platform does not rely on a host firewall to close it. The only
public listeners are the reverse proxy's 80 and 443, Foghorn's external gRPC listener,
which edges outside the mesh dial, the WireGuard UDP port, SSH, and an edge's media
ports (RTMP, SRT, WebRTC, RTSP and DTSC; edges do not join the mesh). An edge's
MistController API, MistServer HTTP, Helmsman and vmagent listen on loopback.

Edge listeners, with every off-host caller:

| Listener                      | Bind            | Callers                                                                                                                                                                                                                                                                                                            |
| ----------------------------- | --------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------ |
| Caddy 80 / 443 (TCP and QUIC) | every interface | Viewers and players (HLS, DASH, WHEP/WHIP signalling, progressive, WebSocket outputs under `/view`); peer Helmsmans' artifact relay reads and another node's clip source reads (`/view`, Foghorn's `source_base_url`); Foghorn's activation probe; Cloudflare's edge-pool monitor (`https://<edge domain>/health`) |
| MistServer RTMP 1935          | every interface | Publishers (RTMP ingest), RTMP viewers                                                                                                                                                                                                                                                                             |
| MistServer RTSP 5554          | every interface | RTSP viewers                                                                                                                                                                                                                                                                                                       |
| MistServer DTSC 4200          | every interface | Peer edges' Mist pulling a stream (`dtsc://<edge host>:4200`, from Foghorn's `STREAM_SOURCE` answer, same cell and cross-cell over the internet), DTSC viewers                                                                                                                                                     |
| MistServer SRT 8889/udp       | every interface | Publishers (SRT ingest), SRT viewers                                                                                                                                                                                                                                                                               |
| MistServer WebRTC 18203/udp   | every interface | WebRTC media for viewers and WHIP publishers (signalling goes through Caddy)                                                                                                                                                                                                                                       |
| MistServer HTTP 8080          | loopback        | Caddy, Helmsman (`MISTSERVER_HTTP_URL`), `frameworks edge diagnose`                                                                                                                                                                                                                                                |
| MistController API 4242       | loopback        | Helmsman, the edge vmagent, the CLI over SSH                                                                                                                                                                                                                                                                       |
| Helmsman 18007 / 18017        | loopback        | Caddy (`/webhooks`, `/_mist`, `/health`, `/internal/artifact`) and Mist triggers on 18007; the local management API on 18017                                                                                                                                                                                       |

The media listeners stay public because their callers have no other path: edges are not on
the mesh, so a peer edge in another cell pulls DTSC from the source edge's public address,
and media clients speak the protocol directly. Each connection is authorized by MistServer
triggers that Helmsman answers through Foghorn and that fail closed when Foghorn is
unreachable (`onfail: deny`): `USER_NEW` admits every viewer connection on every output,
DTSC included (a peer's accepted origin pull is admitted as that pull, everything else as a
viewer under the stream's playback policy), and `PUSH_REWRITE` admits an incoming push only
with a valid stream key over an ingest protocol (RTMP, SRT, WHIP). The HTTP
connector binds loopback because every HTTP caller goes through Caddy's TLS; a plain-HTTP
listener on the public interface would bypass it. Helmsman sets the HTTP connector's
`interface` when it reconciles Mist's protocols, on native and container edges alike, so an
existing edge moves to loopback on its next config reconcile; a container edge on Docker
Desktop publishes only the public set.

| Listener                                                                  | Bind                                                                                                                                      | Set by                                                                                     |
| ------------------------------------------------------------------------- | ----------------------------------------------------------------------------------------------------------------------------------------- | ------------------------------------------------------------------------------------------ |
| Every FrameWorks Go service's HTTP, gRPC, metrics and internal HTTPS port | loopback + WireGuard                                                                                                                      | `BIND_ADDRS` (`pkg/config` `ListenAddrs`, `pkg/server` `RunSpec.BindAddrs`), with FreeBind |
| Foghorn external gRPC `:18029`                                            | every interface                                                                                                                           | `AllInterfaces` on the listener                                                            |
| Foghorn internal HTTP `:18027`                                            | loopback                                                                                                                                  | `FOGHORN_INTERNAL_HTTP_BIND_ADDR`                                                          |
| Privateer DNS `:53`                                                       | loopback                                                                                                                                  | `privateer.socket`                                                                         |
| Livepeer gateway HTTP / CLI                                               | loopback (Docker: HTTP published on loopback + WireGuard, CLI on loopback)                                                                | `http_addr`, `cli_addr`                                                                    |
| Livepeer signer HTTP                                                      | WireGuard (the rollout gate and vmagent probe it there)                                                                                   | `http_addr`                                                                                |
| PostgreSQL                                                                | loopback + WireGuard; HBA admits password logins from the mesh CIDR                                                                       | `postgres_listen_addresses`, `postgres_hba_mesh_cidr`                                      |
| YugabyteDB master/tserver RPC, web UIs and YSQL                           | WireGuard (YSQL takes one address; node-local admin clients connect there, `fw_ysql_host`); HBA admits password logins from the mesh CIDR | `yugabyte_node_address`, `yugabyte_hba_client_cidr`                                        |
| YugabyteDB YCQL                                                           | loopback                                                                                                                                  | `tserver.conf.j2`                                                                          |
| ClickHouse server ports, Keeper client and Raft ports                     | loopback + WireGuard                                                                                                                      | `clickhouse_listen_hosts` (`listen_host`, `interserver_listen_host`)                       |
| Redis / Valkey and Sentinel                                               | loopback + WireGuard                                                                                                                      | `bind`                                                                                     |
| Kafka broker and controller listeners                                     | WireGuard (Kafka needs a distinct port per listener; local tools dial the WireGuard address)                                              | `kafka_bind_host`                                                                          |
| Kafka and MirrorMaker JMX                                                 | none (remote JMX agent off; MirrorMaker's in-process exporter on loopback)                                                                | `KAFKA_JMX_OPTS` in the units                                                              |
| VictoriaMetrics, vmauth, Alertmanager                                     | loopback + WireGuard                                                                                                                      | `prometheus_stack_listen_addrs`                                                            |
| vmagent, vmalert, Prometheus, node_exporter                               | loopback                                                                                                                                  | `prometheus_stack` templates                                                               |
| nginx plain-HTTP site copies `:18090`                                     | loopback                                                                                                                                  | `nginx_http_bind_address`                                                                  |
| Docker-mode services, Listmonk, Chatwoot                                  | published on loopback + WireGuard (Foghorn external gRPC on every interface)                                                              | `composePublishAddrs`                                                                      |

Docker's forwarding rules sit ahead of the host firewall, so a port published without an
address answers on every interface; Compose-deployed services therefore publish each
listener on explicit addresses while the container itself binds every interface of its
own network namespace (it gets no `BIND_ADDRS`). A container reaches the host's PostgreSQL
and Redis on the WireGuard address, since neither listens on the Docker gateway.

The WireGuard address exists only once Privateer brings `wg0` up. Go services bind it with
FreeBind, and the Privateer role sets `net.ipv4.ip_nonlocal_bind` and
`net.ipv6.ip_nonlocal_bind` on every mesh host, so PostgreSQL, Kafka, YugabyteDB,
ClickHouse, Redis and the metrics stack bind it at boot and accept once the mesh is up.

A host image's stock `mistserver.service`, which runs every Mist connector on every
interface, is stopped and masked on every host by `node_baseline`; MistServer runs only as
an edge's `frameworks-mistserver` unit. `frameworks cluster doctor` lists, per host, every
listener bound to every interface outside the public set (SSH, the reverse proxy's 80/443
on a proxy host, Foghorn's 18029 on a Foghorn host, the WireGuard port), so an exposure
does not go unnoticed.

A host without a WireGuard address binds loopback alone. That is a single-host cluster:
`cluster provision` refuses a manifest of more than one host without Privateer, since its
hosts could not reach each other's services.

Public HTTP remains a Caddy concern unless a service explicitly owns an HTTP
endpoint. Foghorn's viewer redirect and MistServer pull-routing surfaces are
separate from the gRPC split.

## Client Authority

gRPC clients construct an explicit dial tuple: address, ServerName, CA material,
and insecure policy. The address is the network route; the ServerName is the TLS
identity. These are not interchangeable, and the ServerName must name the
certificate the dialed endpoint actually serves, not the service's usual internal
name. A client dialing a public media FQDN such as
`foghorn.media-eu-1.frameworks.network` verifies that FQDN against the Web PKI; it
must not carry `foghorn.internal` over from an internal-listener config. A client
dialing a raw WireGuard address or IP still needs the ServerName that appears in
the certificate's SANs, because the address itself carries no identity. This is
why the tuple belongs to endpoint resolution rather than to a process-level
default.

Helmsman's control stream follows the same rule when it fails over between the
Foghorn instances of its cell. `FOGHORN_CONTROL_ADDR` lists the instances (or
names one host that resolves to all of them); Helmsman resolves each entry before
every dial and dials the resolved addresses one by one, but the ServerName and
the `:authority` stay those of the configured entry, never the resolved IP.

`pkg/grpcutil.ClientTLS` enforces the tuple. When a custom CA path or inline CA
PEM is configured it requires a non-empty ServerName (or `DefaultServerName`) and
fails closed otherwise; custom CA material is appended to the system pool, so a
client configured with the internal CA still verifies public certificates
normally. Client packages in `pkg/clients/` export canonical service names but do
not read environment variables. Entrypoints read environment and pass fully
specified client config.

`GRPC_TLS_SERVER_NAME` is not a process-wide runtime knob. Multi-client services
such as Bridge must pass one ServerName per downstream client.

`GRPC_ALLOW_INSECURE` is not a production option: both `ServerTLS` and
`ClientTLS` return an error when it is set and `config.IsProduction()` is true.
Transport TLS is therefore mandatory in production even though the helpers accept
a plaintext mode for dev and test.

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
| `BIND_ADDRS`                                                  | Addresses a Go service's listeners bind (empty: every one)     |
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

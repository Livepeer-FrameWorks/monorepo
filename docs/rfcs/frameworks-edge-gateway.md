# RFC: FrameWorks Edge as the Self-Hosted Gateway

## Status

Draft

Parked by the owner on 2026-09-18 as a later add-on. This document records the full idea so it can be picked up without
repeating the research. It has two parts. Part 1 is the gateway foundation. Part 2 is the device management platform,
which ships separately and builds on Part 1. Items that belong to Part 2 are marked "(Part 2)" throughout.

## TL;DR

- The self-hosted edge bundle (Caddy, MistServer, Helmsman) is the customer-side gateway. It is the same bundle that
  runs platform edges, enrolled into a tenant's virtual private cluster and served by a platform-run Foghorn.
- A node is a list of endpoints (public, LAN) with an observed state per endpoint. It is not a "public" or a "local"
  node kind.
- A node behind a firewall stays connected as outbound-only. Today it is disconnected at activation.
- Viewers who share a node's public egress IP are steered to that node's public endpoint, with a ranked fallback list
  and node failover in the player. The LAN address is used only by an explicit "play on local network" action and by
  integrators, never automatically in a browser.
- A stream ingested on a firewalled node reaches the platform through a Foghorn-arranged DTSC relay push. The MistServer
  fork can do this today; it needs DTSC over TLS before it crosses the internet.
- Enrolment works from a token alone: container start, a native `helmsman install`, short-lived tokens that tenants can
  list and revoke.
- Self-hosting is free and unlimited on every tier. Custom domains, platform delivery, platform transcoding and platform
  storage are what is paid.
- (Part 2) Device discovery, a tenant device inventory with credentials sealed to the node, device-bound ingest, PTZ,
  and playout to LAN devices build on Part 1 as a separately shipped feature.

## Current State

Paths are monorepo paths unless prefixed `mist:`, which refers to the MistServer fork
(`/Users/stronk/frameworks/mistserver`, a separate repository). Line numbers are the ranges read during the research on
2026-09-18 and will drift.

### Topology that must stay true

Every Foghorn (`api_balancing`) is platform-run. A customer "private cluster" is a virtual cluster
(`cluster_class='tenant_private'`, with `control_cell_id` naming the platform cluster that serves it), created by
`createOwnedPrivateCluster` (`api_tenants/internal/grpc/server.go:9301-9371`,
`api_tenants/internal/database/quartermasterdb/private_cluster_adapter.go:61-95`). Customer-operated edge nodes enrol into
it. Authority is node-to-cluster membership plus Quartermaster's cluster-to-tenant entitlement.

### Enrolment

- `frameworks edge deploy` and `frameworks edge provision`, including `--local`, run the Ansible edge role. They need
  `ansible-playbook` on `PATH` (`cli/pkg/ansiblerun/executor.go:138-143`), which the release tarball and the macOS
  package do not ship. The `ansible/` tree itself is embedded in the CLI binary and extracted to the user cache on
  first use (`cli/internal/runtimeassets`). The macOS tray's Provision button shells out to `edge provision --local`
  (`app_mac/Sources/UI/ProvisionView.swift:86-95`) and inherits the `ansible-playbook` requirement.
- The container path (`frameworks edge init` then `edge enroll`, `cli/cmd/edge.go:228-385`) needs Docker and the CLI.
  The image cannot start from a token alone: Helmsman requires `NODE_ID`, `FOGHORN_CONTROL_ADDR`, `EDGE_PUBLIC_URL` and
  `MISTSERVER_URL` at start (`api_sidecar/internal/config/env.go:95-127`), and nothing under `api_sidecar/` or `edge/`
  calls Bridge's `bootstrapEdge`.
- `bootstrapEdge` is a public, token-gated Bridge mutation that proxies Foghorn's `PreRegisterEdge`
  (`cli/cmd/edge_bootstrap.go:21-52`, `api_gateway/internal/resolvers/infrastructure.go:1574-1659`,
  `api_balancing/internal/control/server.go:9842-9930`). `PreRegisterEdge` creates no rows.
- First node of a new cluster: Navigator issues the cluster wildcard certificate only in its periodic reconciler
  (`api_dns/internal/worker/dns_reconciler.go:332-370`), and Foghorn replaces its served certificate set only at listener
  start and in an hourly loop (`api_balancing/internal/control/server.go:658-677`,
  `api_balancing/cmd/foghorn/main.go:1940`). Until then both Bridge and Helmsman fail TLS verification against
  `foghorn.<slug>.<root>`. Derived from the code paths, not reproduced.
- On a cell with several Foghorn replicas, a replica that has not yet learned the cluster returns `PermissionDenied`
  from Quartermaster (`api_tenants/internal/grpc/server.go:6399-6405`), which Foghorn reports to the box as "enrollment
  token invalid or expired" (`api_balancing/internal/control/server.go:77-88`, `:1803-1818`). The token is valid.
- Node identity: `node_id` is `UNIQUE` across the platform (`pkg/database/sql/schema/quartermaster.sql:284`) and derives
  from the hostname (`deriveEdgeNodeID`, `api_tenants/internal/grpc/server.go:6322`). Two tenants with a box named
  `ubuntu` collide and the second retries forever.
- A tenant can own one private cluster: `tenants.max_owned_clusters DEFAULT 1`
  (`pkg/database/sql/schema/quartermaster.sql:72`), enforced at `api_tenants/internal/grpc/server.go:9311-9322`; nothing
  writes the column. This is a limit, not a defect, and this RFC keeps it: a customer location is nodes inside that one
  cluster.
- Tenants have no surface to switch on private-network pulls for their own cluster. `allow_private_pull_sources`
  defaults to false (`pkg/database/sql/schema/quartermaster.sql:213`) and its only writer is the admin CLI
  (`cli/cmd/admin.go:1154`). Tenant self-service PULL streams restricted to the tenant's own cluster and nodes already
  exist (`api_control/internal/grpc/stream_source_location.go:106-160`). An owner-consent flow with revisions and review
  tokens exists in `api_tenants/internal/grpc/media_capacity_consent.go` and does not carry this flag.

### Activation, reachability and addresses

- A fresh enrolment sets `ProbeVerified=false` and starts `probeEdgeActivation`, which requests
  `https://<edge_domain>/` twelve times at five-second intervals and on failure sends `ACTIVATION_FAILED` and drops the
  connection (`api_balancing/internal/control/server.go:2060-2066`, `:9488-9575`). `ProbeVerified` gates `isActive`
  (`api_balancing/internal/state/stream_state.go:3331`). The post-update warm-up probe has the same dependency
  (`api_balancing/internal/control/server.go:6337-6360`). A node behind a firewall therefore cannot stay enrolled.
- Every other platform-to-edge dependency already runs outbound from the edge: updates, storage transfers, thumbnails,
  metrics and TLS bundle delivery. Customer edges do not join the WireGuard mesh and Navigator does not probe edges
  (`~/frameworks/research/reports/gateway/02_private_delivery_nat.md`).
- `infrastructure_nodes.external_ip` is set from the gRPC peer or `x-forwarded-for` at first bootstrap
  (`api_tenants/internal/grpc/server.go:6491-6509`). `ReportAliveNodes` can update it
  (`api_tenants/internal/database/quartermasterdb/report_alive_adapter.go:67-77`), but Foghorn only sends an IP literal
  parsed from the node's base URL (`api_balancing/internal/state/node_dns.go:77`), which is a hostname in production, so
  the address never refreshes. Only A records are written for edges.
- Foghorn holds the control stream's peer address as `NodeState.BinHost`, set on every authenticated Register
  (`api_balancing/internal/control/server.go:1664-1682`, `:1945`), tagged `json:"-"` and therefore neither persisted nor
  replicated (`api_balancing/internal/state/stream_state.go:453`).
- `x-forwarded-for` on the control stream is read unconditionally (`api_balancing/internal/control/server.go:122-129`,
  `:1668-1675`). No ingress render path fronts Foghorn's control port, so the header is purely client-supplied.
- Helmsman reports the node's LAN addresses in Register (`collectNodeFingerprint`,
  `api_sidecar/internal/control/client.go:2262-2287`; `pkg/proto/ipc.proto:763-772`). Quartermaster drops them
  (`upsertEdgeNodeFingerprint`, `api_tenants/internal/grpc/server.go:6810-6836`). The report includes docker, bridge and
  WireGuard interface addresses.

### Viewer routing

- Viewer geography is Foghorn's heaviest scoring input (`GEO_WEIGHT=1000`, GeoIP lookup of the trusted client IP;
  `docs/architecture/viewer-routing.md`). The gap is narrower than "routing does not know where the viewer is": the
  client IP travels as `ViewerEndpointRequest.viewer_ip` (`pkg/proto/shared.proto:300-310`) and Foghorn uses it for the
  GeoIP lookup (`api_balancing/internal/grpc/server.go:2670-2679`), but for live content the placement request carries
  `Location` only (`api_balancing/internal/grpc/server.go:2793-2796`,
  `api_balancing/internal/control/viewer_placement.go:26-30`). Foghorn cannot tell that a viewer shares a NAT with a node.
- `ViewerEndpointResponse` has `primary` and `repeated fallbacks` (`pkg/proto/shared.proto:405-409`). Foghorn fills
  `fallbacks` only for artifacts (`api_balancing/internal/control/playback.go:776-777`); live returns the primary only
  (`api_balancing/internal/control/viewer_placement.go:185-189`). The player decodes `fallbacks` but never fails over to
  another node (`npm_player/packages/core/src/core/PlayerController.ts:524-529`,
  `npm_player/packages/core/src/core/PlayerManager.ts:958-998`,
  `npm_player/packages/core/src/core/ViewerProtocol.ts:67`).
- A private cluster serves any viewer of an entitled tenant. Placement has no viewer-network dimension
  (`02_private_delivery_nat.md`).

### Admission and edge authority

- Helmsman installs six blocking Mist triggers (`api_sidecar/internal/config/manager.go:476-513`,
  `docs/architecture/mist-trigger-contract.md:34-41`). Every public `PLAY_REWRITE`, viewer `USER_NEW` and `PUSH_REWRITE`
  is forwarded to Foghorn with a four-second budget and at most three attempts
  (`api_sidecar/internal/control/client.go:525-541`, `:644-735`; `api_sidecar/internal/handlers/handlers.go:741-742`).
  Helmsman answers locally only for platform-internal names (`handlers.go:845-861`), and the trigger contract forbids
  replaying an unsigned mapping during an outage (`mist-trigger-contract.md:57`).
- Signed tenant and stream authority stops at Foghorn. It refreshes after ten minutes and expires hard after 24 hours
  (`api_control/internal/grpc/media_authority.go:35-36`, `docs/architecture/media-authority.md:208-223`). "Media keeps
  serving when the control plane is unreachable" is a property of Foghorn, not of an edge. An edge that cannot reach a
  Foghorn denies new viewers and publishers; established sessions continue.

### Inter-node media

- All inter-node replication is a DTSC pull to the origin's public host on port 4200, in cleartext
  (`02_private_delivery_nat.md`). A firewalled node can pull from a platform node; a platform node cannot get a stream
  from a firewalled node.
- The fork can push DTSC to another Mist: `MistOutDTSC` with a `dtsc://host[:port]/stream?pass=` target
  (`mist:src/output/output_dtsc.cpp:19-52`, `:176-206`), started through the controller `push_start` API
  (`mist:src/controller/controller_api.cpp:2088-2176`). The receiver writes into a `push://` buffer through the same
  `Output` path RTMP and SRT use, passing `PUSH_REWRITE` (`mist:src/output/output_dtsc.cpp:411-458`). All tracks and their
  metadata are announced and adopted (`mist:src/output/output_dtsc.cpp:105`, `:199-205`, `:306-365`).
- A relayed DTSC push would be denied today twice: the stream name is not a stream key, and `mist.IngestProtocol`
  rejects connector `DTSC` (`pkg/mist/ingest_protocol.go:14-29`). It would also be rejected as `DUPLICATE_INGEST` by the
  ingest session mint (`api_balancing/internal/triggers/processor.go:2040-2067`).
- DTSC has no wire security. The push password travels in a cleartext packet (`mist:src/output/output_dtsc.cpp:83-90`),
  the push client requires the `dtsc` scheme (`:22`, `:31`), and the pull side's `dtscs` branch is unreachable
  (`mist:src/input/input_dtsc.cpp:21-22`, `:147`).
- The fork's C++ hole punching is SRT rendezvous (`srt-fh://`, UDP 7077) and is unused by the Go side
  (`docs/rfcs/nat-traversal.md`).

### Auto-update, names, cookies, pricing

- Edge auto-update does not run for tenant-created clusters. `ReconcileReleaseTargets` iterates only the rows of
  `cluster_release_targets` (`api_balancing/internal/orchestrator/reconciler.go:77-93`); the only writer is the operator
  CLI, which loops the manifest's clusters (`cli/cmd/cluster_releases.go:388-417`); neither private-cluster creation path
  writes a row. `website_docs/src/content/docs/hybrid/node-cli.mdx:62` and the registry row `edge-auto-update`
  (`docs/platform-features.yaml:1091`) say it works.
- Names for a tenant cluster: `edge-<node>.<slug>.<root>`, `edge.<slug>.<root>`, `foghorn.<slug>.<root>`
  (`pkg/dns/edge.go:12-25`), with a wildcard certificate for `*.<slug>.<root>`
  (`api_dns/internal/logic/cert.go:828-848`). Tenant domains exist: `{sub}.cdn.<root>` with its own certificate bundle
  (`pkg/dns/public_services.go:85`, `:227-240`; `api_dns/internal/logic/cert.go:745-762`) and bring-your-own custom
  domains (`api_dns/internal/logic/custom_domain.go:18-23`, `:88-99`). A customer-run edge receives the `cluster:<slug>`
  bundle and the `tenant:<id>` bundles for its cluster (`api_balancing/internal/control/server.go:5541-5567`).
- Session cookies are set with `Domain=<root>` (`cli/cmd/cluster_provision.go:7025-7027`,
  `api_gateway/internal/handlers/auth.go:117-124`, `:174-193`), and customer-run edges are named under that root.
  Inferred from browser standards, not tested: a browser holding a platform session sends those cookies on WebSocket
  handshakes and navigations to a customer's box. Both browser apps already call Bridge cross-origin with credentials,
  and only `website_application/src/hooks.server.ts:5-11` reads the cookie on the webapp host.
- `ListClusters` without pagination returns the first 50 clusters (`pkg/pagination/cursor.go:17`). Ten callers rely on
  it, including Navigator's DNS and certificate loops (`api_dns/internal/logic/dns.go:293`, `:818`;
  `api_dns/internal/worker/dns_reconciler.go:337`).
- Usage on a tenant's own cluster is priced at zero by ownership (`api_billing/internal/pricing/resolve.go:94-110`);
  private clusters are created `free_unmetered` (`private_cluster_adapter.go:88`). `zeroPricedRulesFromTier` zeroes every
  rate, including rates pinned to a platform-operated `execution_backend`. No tier prices transcoding today, so this is a
  structural defect, not a revenue one.
- Inferred, not reproduced: the session stop on tenant suspension fires once. `threshold.go` returns before the terminate
  call when no row changed (`api_billing/internal/handlers/threshold.go:115`), and a failed call is only logged.
- The marketing pricing page sells "Fully self-hosted" with the control plane and "burst into hosted capacity"
  (`website_marketing/src/components/pages/Pricing.jsx:212-216`), while the registry rows `byo-media-cluster`
  (`docs/platform-features.yaml:1248`) and `sovereign-self-host` (`:1272`) say unsupported.

### Devices (Part 2)

- The fork has ONVIF, VISCA and NDI discovery and a controller API (`camera_list`, `camera_config`, `camera_update`,
  `camera_query`, `camera_presets`, `camera_create_stream`, `camera_associate`;
  `mist:src/controller/controller_api.cpp:2230-2242`, `mist:src/controller/controller_discovery.cpp`). It is a preview
  integration outside the fork's proof audit (`mist:FORK_AUDIT.md:17`, `:221`, `:230-233`): the audit proves only that it
  builds.
- Mist writes device passwords in clear into its config (`mist:lib/device.cpp:84-85`, `controller_discovery.cpp:537-543`)
  and `camera_create_stream` uses the discovered URI without credentials (`:2216-2220`), so a camera that requires RTSP
  authentication fails. There are no device triggers; a consumer must poll `camera_list`.
- Helmsman pins `device_discovery = false` (`api_sidecar/internal/config/manager.go:533-551`). The monorepo has no other
  camera code: `pkg/mist` has no camera call.
- The edge ships ONVIF, VISCA, V4L2 (Linux) and the NDI code without the NDI runtime (`edge/Dockerfile:1-22`). RIST is
  compiled out of release builds (`mist:.github/workflows/build.yml:404`). Mist has no SDI, HDMI or audio output, and
  `MistInAV` reads files only (`mist:src/input/input_av.cpp:16-20`, `mist:meson.build:316-325`).
- Push targets run only on the origin node, accept only `rtmp`, `rtmps` and `srt`, and reject private and multicast
  hosts through process environment variables (`docs/architecture/multistreaming.md`,
  `api_control/internal/grpc/server.go:7467-7497`, `pkg/restream/policy.go:13-31`).
- No registry row covers playout, signage or decoders. `device-discovery-contribution-inputs`
  (`docs/platform-features.yaml:187-203`) is roadmap and depends on `edge-clusters`.

## Problem / Motivation

The owner's direction (2026-09-18): the self-hosted edge node is the gateway. It is not only a way to build a private
cluster. Expect many small deployments of one to three nodes at a customer location, often behind a firewall, run by
people who are not operators. Private ingest exists; private delivery completes it. With device management enabled the
same node discovers and manages the customer's A/V devices, ingest first and playout on the same plumbing. The API and
MCP must cover all of it.

Today that customer cannot install a node without a repo checkout and Ansible, cannot keep a node enrolled behind a
firewall, never receives updates on a cluster they created, cannot allow a pull from their own LAN, and gets no benefit
from a node sitting next to their viewers.

## Goals

- A customer gets a node running from the dashboard, on the box itself, without a repo checkout, Ansible or an operator.
- A node without inbound reachability stays enrolled, is visibly "outbound-only", and is never handed to viewers or
  pulling nodes that cannot reach it.
- Viewers behind a node's router are served by that node when it is healthy, and by platform edges otherwise, with no
  customer configuration.
- A stream ingested on a firewalled node is available to the platform for public delivery, recording and processing.
- Tenant-created clusters auto-update through the existing reconciler.
- (Part 2) A tenant can discover, adopt, stream from and control devices on the node's LAN, and route streams to LAN
  output devices, with credentials that stay on the customer's side after storage and with an audit trail.

## Non-Goals

- A customer-run Foghorn, now. Every Foghorn is platform-run and customer clusters are virtual. Offline operation is the
  long-term case for a customer-hosted Foghorn (see Proposal 1.7 and Open Questions).
- Edge-side admission state. The edge stays stateless; no Helmsman decision cache.
- A "site" entity, pairing codes, or a separate registrable domain for edge names.
- Automatic use of LAN addresses by the web player.
- Device management as part of the foundation. It is Part 2 and ships separately.
- Compute routing (which node or supplier runs a processing job). It follows storage placement; see
  `docs/rfcs/processing-orchestration.md` and `docs/rfcs/placement-policy-engine.md`.
- ONNX and AI workloads. A separate feature (`live-ai` registry row) that also needs Livepeer work.
- Tenant budgets, caps and usage tags. They belong to `usage-caps` and `docs/rfcs/stream-balances.md`.
- Full NAT hole punching. `docs/rfcs/nat-traversal.md` keeps that scope.

## Proposal

### Part 1: gateway foundation

#### 1.1 Master defects first

These are owed regardless of the gateway and unblock it.

- **Auto-update for tenant clusters.** A `tenant_private` cluster with no `cluster_release_targets` row inherits the row
  of its `control_cell_id`. `ClusterReleaseTarget` gains `inherited_from_cluster_id`; `ListClusterReleaseTargets` adds
  synthesized rows and `GetClusterReleaseTarget` falls back to the join
  (`api_tenants/internal/database/quartermasterdb/release_adapter.go`). The reconciler is unchanged except that the
  `nodesInCluster` check moves ahead of `ListEdgeReleases`. `SetClusterReleaseTarget` never persists an inherited row.
  This needs no backfill, follows the platform's channel, pin and pause, and keeps owner overrides.
- **`ListClusters` paging.** One `ListAllClusters` helper in `pkg/clients/quartermaster/`, used by all ten callers.
- **Session cookie scope.** Host-only `__Host-` cookies on the Bridge origin, with no `Domain`. No ingress, CORS or docs
  site change is needed because both browser apps already call Bridge cross-origin with credentials. `/auth/refresh`
  accepts the legacy cookie for one release and every auth response expires the legacy `Domain` cookies.
  `hooks.server.ts` stops reading the cookie.
- **Node IDs.** `RuntimeEdgeNodeID(hostname, tenantID, clusterID)` in `pkg/dns/edge.go`: sanitised hostname plus eight hex
  characters of a SHA-256. Deterministic, so re-deploying a box converges on the same ID, which a random suffix would
  not, because `PreRegisterEdge` creates no rows. Legacy raw IDs in the same cluster keep working.
- **Suspension stop.** Foghorn sweeps the tenants of its live streams against its billing-status cache and re-sends
  `StopSessions`; it also re-sends when a node registers.
- **Zero-price rule.** Keep rates pinned to a platform-operated `execution_backend`.
- **Pages.** Reconcile the pricing page, `SovereigntyNote.jsx`, `roadmap.mdx` and the `livepeer-signer` row with the
  registry.

#### 1.2 Endpoint model and reachability

A node has endpoints, each with a scope and an observed state:

- `public`: the existing `edge-<node>.<slug>.<root>` name. State `unknown | reachable | unreachable`, observed by the
  platform.
- `lan`: addresses self-reported by Helmsman (with docker, bridge and WireGuard interfaces filtered out) and stored with
  the node. Never probed by the platform.

The node never asserts that it is publicly reachable, and the platform never invents an address the node did not report.
This follows the pattern of Consul tagged addresses, Kubernetes node addresses and AutoNAT v2's per-address verification
(`~/frameworks/research/reports/gateway/06_lan_and_public_access_research.md`).

- **Egress IP.** Taken from `peer.FromContext` on every authenticated Register. Both `x-forwarded-for` reads are deleted:
  no proxy fronts the control port, so there is no trusted proxy to parse against. The value is persisted and sent
  through the existing `NodeAliveness` path. Quartermaster refreshes `external_ip` only for
  `enrollment_origin='runtime_enrolled'` rows; for `gitops_seed` rows it remains a stable key.
- **Activation.** New control messages `ActivationCheckRequest` and `ActivationCheckResult`. Helmsman makes a loopback
  HTTPS request to its own Caddy with SNI set, and Foghorn compares the leaf fingerprint with the bundle it issued. The
  same check replaces the post-update warm-up probe. Nodes on the older protocol version keep the external probe without
  the disconnect.
- **Classification.** The external probe dials the observed egress IP with SNI set and does not resolve DNS; a DNS-based
  probe is circular once unreachable nodes are left out of DNS. Demotion needs three consecutive failures over at least
  ten minutes; the current state is held when more than half of the probed nodes fail together, because that points at
  the prober. No NAT type is stored: RFC 5780 says it cannot be determined reliably. Existing active edges are
  backfilled to `reachable`, since they all passed the old probe.
- **Exclusions.** The public DNS pool requires `reachable`. One predicate, `state.SourceReachableFrom(src, dst)` (true
  when the source is reachable, or when both nodes share an egress IP), is applied wherever a pull source is chosen:
  federation, `/source`, DVR source pull and relay resolve.
- **Surfaces.** `InfrastructureNode.publicReachability` in GraphQL, a badge in the webapp ("Reachable publicly" or
  "Outbound-only"), MCP `get_node_info`, and `frameworks edge status`.

#### 1.3 Enrolment

- **Token-only container start.** `helmsman bootstrap-edge`, run from the s6 `init-seed` step. With
  `EDGE_ENROLLMENT_TOKEN` set and no persisted bootstrap state, it calls `bootstrapEdge` and persists `NODE_ID`,
  `EDGE_DOMAIN`, `EDGE_PUBLIC_URL`, `FOGHORN_CONTROL_ADDR` and the CA bundle. The token is never persisted. The Bridge URL
  is a build-time default set by the release pipeline from the value the frontend builds already use; `BRIDGE_URL` stays
  a deployment address like `FOGHORN_CONTROL_ADDR`, not a feature toggle.
- **Native install.** `helmsman install --token`, Linux with systemd first, macOS LaunchAgents second. It reuses
  `api_sidecar/internal/edgeseed` and the updater's download, checksum and `ServiceController` seams, and gets its
  component list from `PreRegisterEdge` (resolved from the cluster's effective release target, which depends on the
  auto-update fix). The Ansible native path calls the same installer in its offline form in the same change, so there
  is one installer. `edge deploy` without `--ssh`, `edge provision --local` and the tray's Provision button inherit it.
  Deferred in the first cut: vmagent, ONNX variants, migration from the container stack. `node_tuning` remains the fleet
  path.
- **Tokens.** Default lifetime one hour for tenant actors, maximum 24 hours. New tenant RPCs to list and revoke, filtered
  by tenant; the existing `RevokeBootstrapToken` deletes by ID with no tenant filter and is not exposed. GraphQL, MCP and
  webapp surfaces; the dashboard command runs on the box itself.
- **First-node TLS.** Navigator issues the cluster wildcard certificate on a cluster-scoped `SyncDNS` for `foghorn`,
  asynchronously. Foghorn's `GetCertificate`, on a miss for a served slug, fetches and merges the bundle (singleflight,
  three-second budget, 30-second negative cache). Distinct control errors `ENROLLMENT_CLUSTER_NOT_READY` (retryable) and
  `ENROLLMENT_NODE_ID_TAKEN`, so a valid token is never reported as invalid.
- **Private pulls.** `allow_private_pull_sources` joins the existing owner-consent flow
  (`api_tenants/internal/grpc/media_capacity_consent.go`), only for `tenant_private` clusters, with GraphQL, MCP, webapp
  and CLI surfaces. It is per cluster: enforcement is per cluster, and pinning a stream to a node already exists through
  the placement `nodeIds` selector.

#### 1.4 Own-node delivery

- `ViewerPlacementRequest` carries the viewer IP. A viewer whose IP equals a permitted node's current egress IP prefers
  that node and receives its **public** URL. The customer's router loops that traffic back inside (hairpin NAT), so it
  does not use the WAN link. This is the same signal Apple Content Caching, Microsoft Delivery Optimization's LAN mode and
  Plex's "treat WAN IP as LAN" setting use (`06_lan_and_public_access_research.md`).
- Live resolution fills `fallbacks` with platform edges, and the player gains node failover. The local path is an
  optimisation and never load-bearing.
- The LAN endpoint is published as `lan-<node>.<slug>.<root>`, which the existing cluster wildcard certificate and Caddy
  site cover. It is exposed in the API and used by an explicit "play on local network" action and by integrators. The web
  player never tries it automatically.
  - External facts from web research, not tested by us: since Chrome 142 (October 2025), Edge 143 and Firefox 143, a
    public page that contacts a private address triggers a local-network permission prompt. The address space is decided
    by the resolved IP, so a public name that resolves to a private address triggers it too. Subresource and `fetch`
    requests are covered from the start and WebSockets from Chrome 147. An embedded player needs
    `allow="local-network-access"` on the embedding page's iframe, the prompt is attributed to the embedding origin, and
    a denial persists.
  - Many home routers drop DNS answers that point into private ranges (OpenWrt by default, Fritz!Box). Plex documents
    this as a known failure of the same naming scheme.
- Sessions served to a same-egress viewer get an analytics marker, because they have no useful GeoIP result.
- Known limits: hairpin NAT is missing on some consumer routers (the fallback covers it); carrier-grade NAT gives false
  matches (the node still serves those viewers correctly over its public address); with IPv6 there is no shared public
  address and a prefix comparison is needed.
- A node with no public endpoint serves browsers only through the explicit action. Its streams reach everyone else
  through 1.5.

#### 1.5 DTSC relay push from a firewalled node

- **Trigger.** Foghorn arranges a relay when it admits an ingest on a node whose public endpoint is `unreachable` and the
  stream's policy allows platform delivery. Arranging on first viewer demand does not work: a viewer cannot boot a
  `push://` buffer.
- **Receiver.** Chosen with the existing node scoring, using the geography of the pusher's egress IP, among reachable
  platform nodes of an allowed cluster.
- **Authorisation.** A single-use, short-lived `fwrelay.` HMAC bound to attempt, tenant, stream, source node and
  receiver, modelled on the existing `fwsrc.` pull credential
  (`api_balancing/internal/control/source_pull_capability.go:12-35`), carried as the DTSC password. A new branch at the
  top of `handlePushRewrite` for connector `DTSC` verifies it and returns the internal stream name without key
  validation, the Commodore claim or a session mint, so it cannot trip duplicate-ingest protection. Every other DTSC push
  stays denied. Mist JWT input tokens are not used, because they skip `PUSH_REWRITE`.
- **Source selection.** `/source` answers `push://` for the armed (stream, receiver) pair. The stream registry's
  `Location` gains a `RelayNodeID` so `BuildDTSCURI` and federation name the receiver. Once receiving, the receiver has
  inputs and is not tagged replicated, so it becomes the pull source for platform nodes with no balancer change. Owner,
  DVR and ingest authority stay with the ingest node.
- **Commands.** `StartRelayPush` and `StopRelayPush` reuse Helmsman's existing `PushStart`, `PushList` and `PushStop`.
  Relay pushes are kept out of restream status and Decklog events. Attempts live in a `relay_attempts` table in the
  Foghorn database.
- **Failure.** On `PUSH_END` Foghorn re-arms with a fresh token and may pick another receiver. A return to the same
  receiver is handled by Mist's resume buffer.
- **Fork change.** DTSC over TLS: copy the RTMPS accept pattern onto the DTSC listener, accept `dtscs://` in the push
  client and in the input `source_match`, add a default port. Required before any relay leaves a private network.
- **Metering.** Relay bytes are attributed to the attempt as infrastructure and are not billed. Viewer egress from the
  receiver's cluster bills normally.

#### 1.6 Pricing position

Self-hosting is free and unlimited on every tier, including Free. Own-node ingest, delivery, processing and storage stay
priced at zero as today. Paid: tenant subdomain and custom domain (already gated), delivery from platform edges,
platform transcoding and Livepeer, platform storage, support. The relay push itself is free; the platform delivery it
enables is what is billed.

#### 1.7 Offline operation

Not built here. A short-lived Helmsman decision cache could safely re-admit viewers only for streams with a public
policy, no viewer cap and a postpaid active owner; it cannot cover JWT or webhook policies, viewer caps, prepaid cut-off
or any publisher admission, and it delays every revocation by its lifetime. A location that must run offline is the case
for a customer-hosted Foghorn, which would inherit Foghorn's existing 24-hour authority autonomy with no new edge state.
That reverses today's rule that every Foghorn is platform-run and is left as an open question.

### Part 2: device management platform (ships separately, builds on Part 1)

Device management is available only on clusters the tenant owns. Platform and marketplace clusters never discover
anything. The full design, with protos and table shapes, is in
`~/frameworks/research/reports/gateway/03_devices_ingest_playout.md` sections 3.1 to 3.7.

#### 2.1 Consent

Two more owner-consent permissions next to `allow_private_pull_sources` from 1.3: `allow_private_destinations` (with an
optional CIDR list, replacing the `RESTREAM_ALLOW_PRIVATE_*` environment variables for tenant-owned clusters) and
`allow_device_management`. Defaults are false. They record the customer's decision about their own network; they are
not platform knobs.

#### 2.2 Inventory and credentials

- **Sightings** live in the Foghorn cell database per node, keyed by endpoint ID (`onvif:<uuid>`, `ndi:<name>`,
  `visca:<ip>:<port>`, `v4l2:<path>`), with a ten-minute expiry. They are observed node state, like node health.
- **Adopted devices** live in Commodore (`commodore.devices`, `commodore.device_credentials`), because Commodore owns
  the streams, pull sources and push targets a device relates to and already has field encryption. Quartermaster keeps
  clusters, nodes and consent.
- **Credentials** are field-encrypted at rest (purpose `device-credential`) and delivered sealed to the **node**, not the
  cell: Helmsman registers an X25519 public key and Commodore seals with the existing `pkg/mediaauthority/seal.go`
  construction. Foghorn stores and forwards ciphertext it cannot open. This departs from pull URIs, which the Foghorn
  cell opens. It does not protect against root on the box, and the customer documentation says so.
- **Fork change.** With a managed `device_credentials_volatile` setting, Mist omits usernames and passwords from its
  stored config. Helmsman pushes credentials with `camera_update` and re-pushes after a Mist restart.
- **Lifecycle.** Discovered and unclaimed; adopted; retired. Adoption is never automatic, because WS-Discovery and mDNS
  answers are unauthenticated and adoption is what sends credentials to a node. When two nodes see a device, one control
  node is elected by stable hash and only it receives credentials and executes commands. A device that appears to move
  needs owner confirmation; a move to another cluster always does.
- **Control stream.** `ConfigSeed.device_management`, and new `ControlMessage` payloads for inventory reports, credential
  sync, device commands with results, device streams, output routes with status, and a local command journal.
  `ForwardCommand` gains a command result so commands work across Foghorn replicas.

#### 2.3 Device to stream

`ingest_mode='device'`: Commodore stores a binding (device, profile, transport, always-on), not a URI, and restricts the
stream's source location to the device's cluster automatically. Foghorn's managed-stream reconciler places it on a node
that currently sights the device. Helmsman writes the stream with a placeholder source and answers `STREAM_SOURCE` for
these streams itself, composing the source from the current device address and the locally opened credentials
(`mist:lib/stream.cpp:674-688`). The password is therefore never in Mist's config or in the applied-stream report, an
address change needs only a stream restart, and the tenant never supplies a Mist source string. NDI input is raw video
and carries a managed encode. Nothing new is rated; `source_kind=device` is a reporting dimension only.

#### 2.4 Control

`sendDeviceCommand` maps to Mist `camera_query` and `devicePresets` to `camera_presets`. Motion commands expire after
five seconds and are never queued or retried. `stop` is always accepted. While the control stream is down, Helmsman
accepts a site control token (only its SHA-256 travels in `ConfigSeed`) for PTZ, preset recall and start or stop of
routes that are already applied; such commands are journaled and delivered to the audit log on reconnect. The node acts
on authority it already holds and never widens it.

#### 2.5 Playout

Playout is ingest reversed. Output devices share the inventory with `direction='output'`; most are entered manually,
since NDI receivers and SRT decoders do not announce themselves. `commodore.push_targets` is generalised, not
duplicated: `target_kind` (`external` | `device`), a device reference, an executor cluster and node, a fallback, and the
`ndi`, `tsudp` and `tsrtp` schemes for device targets. A desired-state reconciler sends `ApplyOutputRoutes` to the
executor node, which pulls the stream outbound like any viewer edge and pushes to the LAN device. Destination policy for
device targets comes from the consent in 2.1. One schedule store in Commodore serves `scheduled-recordings`,
`scheduled-vod-streams` and routes; the executor holds the next 48 hours of windows. Helmsman switches a running push to
a slate or backup stream locally, because Mist's `fallback_stream` acts only at output connect time.

Playout needs a slightly different deployment from ingest: a `cap_playout` capability, no public reachability (it
depends on the outbound-only profile from 1.2), layer-2 presence on the device network (native install or the Linux
host-network container; the macOS container is refused), the NDI runtime installed by the customer, decode cost for NDI
output, and no display output on the box itself.

#### 2.6 Vendor adapters

Encoder configuration, destination setup, health and reboot on vendor hardware run in a separate signed process on the
node, `frameworks-device-adapters`, behind a Unix-socket gRPC contract. Core code sees capabilities and `adapter:<verb>`
commands and never a vendor name. No tenant-supplied adapter code.

#### 2.7 Security

`devices:read` and `devices:write` token scopes. High-risk verbs (requiring `mcp:high-risk` with an API token): adopt,
manual add, set credentials, confirm move, retire, `preset_store`, adapter writes, routes to private or multicast
destinations, and creating a site control token. `streams:write` alone does not imply camera control. A tenant-visible
`commodore.device_command_events` table with 90-day retention. LAN controls: a per-cluster CIDR allowlist defaulting to
the node's attached subnets, a fixed error vocabulary, an interface allowlist for probes, and rate limits on adds and on
the offline token path.

## Impact / Dependencies

- **Foghorn (`api_balancing`).** Activation check, reachability classification, egress IP, own-node preference, live
  fallbacks, relay arrangement and the `PUSH_REWRITE` relay branch, certificate merge, suspension sweep. (Part 2)
  sightings store, command relay, route reconciler.
- **Helmsman (`api_sidecar`).** `bootstrap-edge`, `install`, activation check, relay push commands, LAN address
  filtering. (Part 2) device manager, credential store, local control API, slate handling.
- **Quartermaster (`api_tenants`).** Release target inheritance, node ID derivation, token RPCs, consent fields,
  reachability and LAN address columns.
- **Bridge (`api_gateway`).** Host-only cookies, token and consent surfaces, node reachability fields, error codes on
  `bootstrapEdge`. (Part 2) device API and MCP tools.
- **Commodore (`api_control`).** (Part 2) inventory, credentials, device streams, push target generalisation, schedule
  store, command audit.
- **Navigator (`api_dns`).** On-demand cluster certificates, the `lan-` records, pool membership by reachability.
- **Purser (`api_billing`).** Zero-price rule fix, suspension terminate ordering.
- **Player (`npm_player`).** Node failover; the explicit local action.
- **CLI, tray app, Ansible edge role.** One native installer; token flows.
- **Protos.** `pkg/proto/ipc.proto`, `pkg/proto/quartermaster.proto`, `pkg/proto/shared.proto`. GraphQL schema changes
  need the owner's `make graphql-all`.
- **Migrations.** Quartermaster, Foghorn and (Part 2) Commodore, targeting the pending release at build time.
- **MistServer fork.** DTSC over TLS (Part 1). (Part 2) volatile credentials, discovery interface allowlist, response
  size caps, fuzzing of the ONVIF and VISCA parsers, possibly re-enabling RIST.
- **Registry rows.** `edge-clusters`, `enrollment-tokens`, `edge-auto-update`, `byoc-ownership`, `pull-streams`,
  `mac-tray`, `byo-media-cluster`, `sovereign-self-host`, `nat-traversal`. `usage-caps` is referenced, not changed.
  (Part 2) `device-discovery-contribution-inputs` points at Part 2 and gains subitems; one new row for playout routes,
  placed as a subitem of `multistream-targets`; `scheduled-recordings` and `scheduled-vod-streams` note the shared
  schedule store. Each shipped customer-facing unit updates the registry and gets a blog post.

### Relation to existing RFCs

- `docs/rfcs/nat-traversal.md` stays as it is. This RFC places the relay push ahead of hole punching and notes that the
  NAT RFC's WebRTC-viewer motivation does not match what the C++ code does (SRT rendezvous). Rewriting it is left to
  that RFC.
- `docs/rfcs/placement-policy-engine.md`: own-node preference and `SourceReachableFrom` are placement facts, with no
  policy schema change.
- `docs/rfcs/processing-orchestration.md` and `docs/rfcs/workload-cost-model.md`: compute routing, out of scope here.
- `docs/rfcs/stream-balances.md`: budgets, out of scope here.
- (Part 2) `docs/rfcs/flow-automation.md` should reference the shared schedule store and gain device triggers and
  actions.

## Alternatives Considered

- **Public or local node kinds.** One kind per node. Rejected: none of the surveyed systems models it that way, and a
  port-forwarded node is both.
- **Automatic LAN addresses in the web player (the Plex approach).** Rejected for the first version: a permission prompt
  attributed to the customer's site, an iframe attribute the customer must add, a permanent deny, and DNS rebinding
  filters.
- **WebRTC with platform-relayed signalling for LAN-only nodes.** The browser's ICE picks the LAN path without a prompt
  today. Deferred: it needs a signalling relay through the control stream, and the browser vendors plan to extend the
  permission to WebRTC.
- **SRT caller push for the relay.** Rejected in favour of DTSC, which keeps every track and its metadata and reuses the
  existing DTSC pull topology on the platform side.
- **Helmsman decision cache.** Rejected; see 1.7.
- **A separate registrable domain for edge names.** Rejected: tenant domains exist, and the cookie fix removes the
  exposure.
- **An `edge_sites` entity.** Rejected: per-node facts (egress IP, LAN addresses) are enough.
- **A random node ID suffix.** Rejected: it breaks idempotent re-deploy.
- **Inserting a release target row at cluster creation.** Rejected: it hard-codes a channel, ignores a platform pause or
  pin, and needs a backfill.
- **(Part 2) Cell-sealed device credentials, like pull URIs.** About a week cheaper. Rejected: a Foghorn cell database
  would accumulate openable LAN passwords for every customer location.
- **(Part 2) A parallel output-routes system.** Rejected in favour of generalising push targets.

## Risks & Mitigations

- **Wrong reachability demotion removes platform edges from public DNS.** Backfill to `reachable`, asymmetric
  hysteresis, and holding state when most probes fail together.
- **Auto-update switches on for every existing customer node at once.** Each private cluster is its own canary with
  batch size one; release note; check that control cells are not pinned to a release without artifacts for the node's
  platform.
- **Hairpin NAT gaps and carrier-grade NAT false matches.** The fallback list and player failover; both cases still play.
- **Cleartext DTSC.** No relay leaves a private network before DTSC over TLS lands. The single-use, short-lived,
  receiver-bound token limits replay but does nothing for media secrecy.
- **Installer supply chain.** The install script verifies the Helmsman tarball against the signed release manifest the
  updater already trusts; the dashboard shows the container digest.
- **Blocking work inside a TLS handshake (certificate fetch on SNI miss).** Singleflight, a three-second budget, a
  negative cache, and only served slugs trigger a fetch.
- **A customer page is still same-site with Bridge after the cookie fix.** CORS and the JSON preflight keep such
  requests ineffective; never add a wildcard origin.
- **(Part 2) Untrusted SOAP and XML parsed inside `MistController`.** The largest risk in the feature. Fuzz the ONVIF and
  VISCA parsers under ASan, cap response sizes, and consider moving discovery into a child process before enabling it at
  customer locations.
- **(Part 2) Cloud-to-LAN pivot through manual add and route destinations.** Owner or admin plus high risk, consent off
  by default, CIDR allowlist, fixed error vocabulary, rate limits.
- **(Part 2) One compromised tenant admin can steer every device of that tenant.** Audit log, high-risk class, and
  credentials that never reach a node in another cluster without a confirmed move.

## Migration / Rollout

1. Master defects (1.1), as independent changes.
2. Enrolment (1.3): first-node TLS, token-only container start, tokens, native installer, private-pull consent.
3. Firewalled nodes (1.2): egress IP, then activation and reachability, then surfaces. Existing edges are backfilled to
   `reachable`; nodes on the older control protocol keep the external probe without the disconnect.
4. Own-node delivery (1.4) and relay push (1.5), in parallel. The fork's DTSC over TLS lands before relay is enabled
   outside a private network.
5. Documentation: "what your edge sends to the platform" and "what keeps working when the platform is unreachable",
   written from code; corrections to the hybrid docs; registry rows; a blog post per shipped customer-facing phase.
6. (Part 2) Consent permissions; fork hardening and packaging; inventory and credentials; device streams; control;
   surfaces; then playout, the schedule store, slate handling and adapters.

No feature flags for platform-internal behaviour. The cookie change keeps the legacy cookie readable for one release so
sessions migrate without a forced logout. Estimates (one engineer who knows the codebase, excluding review rounds, and
not validated): Part 1 about 20 to 28 weeks; Part 2 ingest about 16 to 21 weeks and playout about 8 to 10 more.

## Open Questions

- The customer-facing name. "Gateway" is clear, but broadcast buyers read "video gateway" as a protocol converter, and
  `api_gateway` and `livepeer-gateway` already use the word in code. A codename in the style of the services is an
  option, for example "Tender". Nothing new needs a name in code.
- When does a customer-hosted Foghorn become worth building? It is the answer to offline operation and reverses the
  current rule that every Foghorn is platform-run.
- Should tenant-declared networks be added later as an override for the same-egress-IP match (several uplinks, several
  offices feeding one node)? Apple, Plex and the enterprise eCDNs all ended up adding one.
- Does Navigator's DNS path accept private-range A records? Not checked. The only private-address guard found restricts
  callers of its internal HTTP endpoints.
- Are the Google Trust ACME fallback credentials set in production? All cluster and tenant certificates share one Let's
  Encrypt registered-domain limit, and the fallback works only with those credentials. Not visible to the research.
- Do the two inferred defects (fire-once suspension stop, cookie exposure in browsers) reproduce? Each needs a failing
  test or a browser check before its fix is sized.
- (Part 2) NDI runtime distribution: customer-installed through a guided step, bundled with attribution, or none. NDI's
  terms for hardware or appliance products were not verified.
- (Part 2) Who may ever read a device password: server-side encryption with node-sealed delivery, or client-side sealing
  to the node key so the platform only stores ciphertext.
- (Part 2) Pricing: free with revenue from what leaves the location, a per-node fee for playout, or per adopted device.
- (Part 2) How far toward a video management system: contribution and production cameras with a soft cap, or
  surveillance bridging as a product.
- (Part 2) Signage on the box itself: Mist has no display output, so signage means a separate player device.
- (Part 2) Which vendor adapter comes first. No vendor API was verified.

## References, Sources & Evidence

- [Source] `~/frameworks/research/reports/report_truefoundry_gateway_lessons.md` (vendor research that started this;
  several of its FrameWorks claims were corrected by the reports below)
- [Source] `~/frameworks/research/reports/gateway/00_gateway_plan.md` (owner-reviewed plan proposal, revision 2)
- [Source] `~/frameworks/research/reports/gateway/01_site_model_enrolment.md` (enrolment, scale, billing; its `edge_sites`
  entity and separate domain were rejected)
- [Source] `~/frameworks/research/reports/gateway/02_private_delivery_nat.md` (activation probe, addresses, inter-node
  media)
- [Source] `~/frameworks/research/reports/gateway/03_devices_ingest_playout.md` (Part 2 design, protos and table shapes)
- [Source] `~/frameworks/research/reports/gateway/04_policy_compute_agents.md` (edge authority, admission, trust page
  content)
- [Source] `~/frameworks/research/reports/gateway/05_followup_code_checks.md` (admission path, own-node delivery minimum,
  tenant domains, cookies, auto-update, DTSC relay push)
- [Source] `~/frameworks/research/reports/gateway/06_lan_and_public_access_research.md` (prior art and browser
  constraints, with source URLs)
- [Source] `~/frameworks/research/reports/gateway/07_build_plan_phases_0_to_3.md` (file-level build plan and corrections)
- [Evidence] `api_balancing/internal/control/server.go` (activation probe, control-stream peer address, `PreRegisterEdge`,
  certificate holder, TLS bundle delivery)
- [Evidence] `api_balancing/internal/orchestrator/reconciler.go`, `cli/cmd/cluster_releases.go`,
  `api_tenants/internal/database/quartermasterdb/release_adapter.go` (release targets)
- [Evidence] `api_tenants/internal/grpc/server.go` (private cluster creation, node bootstrap, fingerprint upsert)
- [Evidence] `api_balancing/internal/grpc/server.go`, `api_balancing/internal/control/viewer_placement.go`,
  `api_balancing/internal/control/playback.go`, `pkg/proto/shared.proto` (viewer IP and fallbacks)
- [Evidence] `npm_player/packages/core/src/core/PlayerController.ts`, `PlayerManager.ts`, `ViewerProtocol.ts`,
  `GatewayClient.ts` (no node failover)
- [Evidence] `api_sidecar/internal/config/manager.go`, `api_sidecar/internal/handlers/handlers.go`,
  `api_sidecar/internal/control/client.go`, `docs/architecture/mist-trigger-contract.md` (admission path)
- [Evidence] `api_control/internal/grpc/media_authority.go`, `docs/architecture/media-authority.md` (authority lifetimes)
- [Evidence] `api_balancing/internal/control/source_pull_capability.go`, `api_balancing/internal/triggers/processor.go`,
  `pkg/mist/ingest_protocol.go`, `api_balancing/internal/handlers/handlers.go` (ingest admission and source selection)
- [Evidence] `api_gateway/internal/handlers/auth.go`, `cli/cmd/cluster_provision.go`,
  `website_application/src/hooks.server.ts` (cookies)
- [Evidence] `pkg/dns/edge.go`, `pkg/dns/public_services.go`, `api_dns/internal/logic/cert.go`,
  `api_dns/internal/logic/custom_domain.go` (names and tenant domains)
- [Evidence] `api_billing/internal/pricing/resolve.go`, `api_billing/internal/handlers/threshold.go` (zero pricing,
  suspension)
- [Evidence] `cli/pkg/provisioner/role_provisioner.go`, `cli/pkg/ansiblerun/executor.go`,
  `api_sidecar/internal/config/env.go`, `edge/Dockerfile` (install paths)
- [Evidence] MistServer fork, not in this monorepo: `src/output/output_dtsc.cpp`, `src/input/input_dtsc.cpp`,
  `src/controller/controller_api.cpp` (DTSC push); `lib/device.cpp`, `lib/device_onvif.cpp`,
  `src/controller/controller_discovery.cpp`, `FORK_AUDIT.md`, `lib/ndi/NOTICE.md` (Part 2)
- [Reference] `docs/architecture/viewer-routing.md`, `docs/architecture/node-enrollment.md`,
  `docs/architecture/edge-deployment.md`, `docs/architecture/media-placement-policy.md`,
  `docs/architecture/multistreaming.md`, `docs/architecture/pull-streams.md`, `docs/architecture/managed-streams-ha.md`
- [Reference] `docs/rfcs/nat-traversal.md`, `docs/rfcs/placement-policy-engine.md`,
  `docs/rfcs/processing-orchestration.md`, `docs/rfcs/workload-cost-model.md`, `docs/rfcs/stream-balances.md`,
  `docs/rfcs/flow-automation.md`
- [Reference] External, from web research and not tested by us: Chrome Local Network Access
  (https://developer.chrome.com/blog/local-network-access, https://wicg.github.io/local-network-access/), Microsoft Edge
  LNA guide (https://learn.microsoft.com/en-us/deployedge/ms-edge-local-network-access), Apple content caching
  (https://support.apple.com/guide/deployment/intro-to-content-caching-depde72e125f/web), Microsoft Delivery Optimization
  (https://learn.microsoft.com/en-us/windows/deployment/do/waas-delivery-optimization-reference), Plex network settings
  (https://support.plex.tv/articles/200430283-network/) and secure connections
  (https://support.plex.tv/articles/206225077-how-to-use-secure-server-connections/), AutoNAT v2
  (https://github.com/libp2p/specs/blob/master/autonat/autonat-v2.md), RFC 5780, RFC 8305, RFC 8445, NDI SDK licensing
  (https://docs.ndi.video/all/developing-with-ndi/sdk/licensing)

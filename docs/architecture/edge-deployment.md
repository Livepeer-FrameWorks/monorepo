# Edge Deployment - Cross-Platform Edge Node Provisioning

The edge stack (Helmsman, MistServer, Caddy) supports Linux and macOS. Linux can run docker-compose or native systemd mode. macOS runs native launchd mode only; local macOS deploys use the user LaunchAgent domain, while remote/system deploys use LaunchDaemons. The CLI detects the target OS/arch over SSH and uses the appropriate service manager and filesystem layout.

## Architecture

```
frameworks edge deploy --ssh admin@target --mode docker
    │
    ├─ Bridge creates or resolves the private cluster + enrollment token
    ├─ PreRegisterEdge resolves node ID, edge domain, pool domain, Foghorn address
    ├─ SSH connect, detect OS/arch
    │   ├─ Linux + docker → compose path
    │   ├─ Linux + native → systemd path
    │   └─ Darwin → launchd native path
    │
    ├─ Preflight checks (OS-aware)
    ├─ Install docker templates or pinned native binaries
    ├─ Write config, internal CA, certs, and service definitions
    ├─ Start services
    └─ Helmsman enrolls with Foghorn through the control stream
```

`frameworks edge deploy` is the operator-friendly path. It can either use the logged-in Bridge flow to create/reuse a private cluster and issue an enrollment token, or accept a pre-existing `--enrollment-token`. `frameworks edge provision` remains the lower-level/admin path for explicit domains, manifests, registration, and certificate fetches.

## Cluster Node Lifecycle

`frameworks cluster nodes ...` is the operator lifecycle surface for edge nodes registered in the FrameWorks control plane:

```
frameworks cluster nodes add --ssh ubuntu@edge-1
frameworks cluster nodes list
frameworks cluster nodes drain --node edge-1
frameworks cluster nodes resume --node edge-1
frameworks cluster nodes remove --node edge-1 --wait 4h
frameworks cluster nodes evict --node edge-1
```

`cluster nodes add` always targets an existing cluster. It defaults to the active context `cluster_id`, prompts for a cluster on TTYs when the context has no default, mints a short-lived enrollment token through Quartermaster, and passes that token directly into the existing edge deployment pipeline without printing it. Platform/provider contexts mint with GitOps-backed service auth and the system tenant. Self-hosted owner contexts mint with the active tenant JWT and let Quartermaster infer the tenant from that JWT. Quartermaster requires owner/operator/provider authority to mint enrollment tokens; ordinary tenant subscriptions can inspect and use cluster capacity but cannot add infrastructure. Lifecycle-managed adds default to native mode and, unless `--version` is supplied, install from the cluster's release target. Clusters without a release target use the stable manifest. Docker mode remains available as an explicit install mode, but it is outside release-target convergence until container rollout support exists. This keeps token handling internal while reusing the established Bridge bootstrap, Quartermaster, Foghorn, and Ansible role path.

Before provisioning, `add` probes the target over SSH. New installs write a `CLUSTER_ID` marker into the edge environment; subsequent runs use that marker to distinguish a clean target, a complete same-cluster install, a foreign cluster, and an existing edge install without a cluster marker. A same-cluster target only short-circuits when the edge stack and node marker are present, Quartermaster still has an active node registration, and Foghorn reports live node health. Partial installs, stale markers, missing registrations, non-active registry status, and unmarked installs require `--force-reapply` after operator confirmation. Foreign clusters are refused.

Node-targeted commands accept `--node <name-or-id>` and use an interactive node picker on TTYs when no selector is provided. `--node-id` remains as a deprecated scripting alias.

`drain` sets the node to `draining`, which stops new placement while allowing existing sessions to finish. `resume` returns the node to `normal`. `remove` is graceful by default: it sets `draining`, waits up to 4 hours for active streams to reach zero unless `--wait` changes the deadline, then sets `maintenance` and marks the Quartermaster registry row `retired`. `remove --wait 0` is rejected; use `evict` for immediate fencing. `evict` immediately sets `maintenance` and marks the registry row `evicted`. Mesh and DNS eligibility already filter to active nodes, so these non-active statuses remove the node from normal routing surfaces without deleting historical identity data.

## Personas

The CLI shape is shared across operator personas, but the auth path is different:

| Persona    | Typical scope                                                            | Node lifecycle auth path                                                                                                                |
| ---------- | ------------------------------------------------------------------------ | --------------------------------------------------------------------------------------------------------------------------------------- |
| platform   | Provider/operator access to platform-official clusters and core services | GitOps-backed service token; lifecycle RPCs intentionally omit user JWT metadata                                                        |
| selfhosted | Tenant-owned cluster footprint, usually edge-only                        | Active tenant JWT against configured owner control-plane endpoints; Foghorn validates JWT/service-token metadata on node lifecycle RPCs |
| user       | Account, billing, insights, Skipper interactions                         | Public Bridge/account APIs; no cluster node lifecycle mutation                                                                          |

Self-hosted is not a separate node type from edge. It is the tenant-owned operator persona for a cluster that usually consists only of edge nodes. Those nodes are still managed by platform Foghorn/Quartermaster. The same `frameworks cluster nodes ...` commands are available when the tenant JWT owns the cluster. Hosted user contexts remain separate: they can inspect account and cluster insights through public account APIs, but they cannot drain, remove, evict, or add infrastructure nodes.

Saved owner gRPC endpoints use explicit transport settings. `frameworks setup` records either TLS or plaintext when it captures self-hosted Quartermaster/Foghorn endpoints; non-local overrides created later with `frameworks context set-url` must use `--tls` or `--allow-insecure`.

## Edge Release State

Quartermaster owns the release catalog and cluster target:

- `quartermaster.edge_releases` stores release-track/version rows with the per-component release manifest. Valid tracks are `stable` and `rc`; `edge` is the component family, not a track.
- `quartermaster.cluster_release_targets` stores the target track/version, pause/resume state, and operational rollout plan for each cluster.

Foghorn owns runtime state:

- `foghorn.node_components` stores component versions reported by Helmsman in `NodeLifecycleUpdate`.
- `foghorn.node_update_state` stores the latest node update phase and error state.

Helmsman reports installed component versions on every lifecycle update. Initial native installs pass Helmsman, MistServer, Caddy, and config-schema versions into the Helmsman environment from the same GitOps manifest that supplied the artifact pins; after an agent-pull update, Helmsman records the applied component version in `/etc/frameworks/component-versions.env` (or the platform-equivalent config directory). Foghorn persists those versions and includes them in node health, so `frameworks cluster nodes list --health` can show component versions next to mode and stream counts.

The Helmsman control stream also carries `DesiredStateUpdate` and `UpdateApplyResult`. Helmsman downloads and checksum-verifies requested artifacts, applies them to the native install path, restarts/reloads the affected component, records the installed version, and rejects drain-required updates unless Foghorn supplied a non-expired cordon token. Foghorn persists component-level results and rejects apply results whose `target_release` no longer matches the node's persisted update target. Successful Mist updates move through `warming`; only after the node reports the expected component versions, sends a fresh healthy lifecycle heartbeat, and passes the edge endpoint probe does Foghorn return it to `normal` mode and mark the update phase `idle`. Failed results mark the node `failed` and leave it fenced for operator inspection.

Foghorn runs an edge release reconciler against Quartermaster's `cluster_release_targets`. It resolves the target release row, diffs desired component versions against `foghorn.node_components`, and pushes direct Helmsman/Caddy updates over the existing Helmsman stream. Config-schema versions are reported for visibility and compatibility checks; runtime configuration changes still flow through the existing ConfigSeed path, not release artifact convergence. Provider provision and `cluster upgrade --all` runs publish the selected GitOps release manifest into `quartermaster.edge_releases` and sync every manifest cluster to the selected track/version. Owner contexts set targets against catalog rows that the provider path already published; `frameworks cluster releases publish` and `frameworks cluster releases target set` are repair/override commands, not the normal release path.
Release rows must include at least one updateable native edge component (`helmsman`, `mist`, or `caddy`); config-schema-only rows are rejected because they do not drive runtime convergence.
Rollout-plan JSON only accepts controls that are implemented by the current reconciler, such as canary, batch size, and error-abort limits. Capacity-floor fields are rejected until disruptive drain rollouts need them.

MistServer is special because most runtime work is process-per-session. A new Mist release can rebuild every binary and still not require a node drain. The default Mist apply strategy is therefore `rolling_stage`: Helmsman downloads and verifies the complete Mist bundle, replaces all Mist binaries atomically so there is no half-updated install state, and sends `USR1` to MistController. Existing ingest/viewer/process instances keep running; new sessions use the replaced binaries.

Foghorn should only enter cordon/drain/warm for Mist when the release manifest carries an explicit machine-generated update contract that requires it. That contract must come from the MistServer release pipeline, not operator CLI metadata and not checksum diffs. The `Livepeer-FrameWorks/mistserver` workflow currently publishes native tarballs and Docker digests; the next required release-pipeline change is to publish a Mist update contract with each release and have the monorepo GitOps manifest import it with the `external_dependencies.mistserver` entry.

## Service Responsibilities

| Component                | Role                                                                    | Data                                             |
| ------------------------ | ----------------------------------------------------------------------- | ------------------------------------------------ |
| Helmsman (`api_sidecar`) | Edge orchestrator, MistServer trigger forwarder, gRPC stream to Foghorn | Operational state, stream counts, trigger events |
| MistServer               | Media server, RTMP ingest, HLS/DASH output, transcoding                 | Stream data, client connections, codec info      |
| Caddy                    | TLS termination, reverse proxy, ACME cert management                    | Certificates, access logs                        |

## Platform Differences

### Filesystem Layout

| Purpose             | Linux                        | macOS system domain                    | macOS user domain                    |
| ------------------- | ---------------------------- | -------------------------------------- | ------------------------------------ |
| Edge workdir        | `/opt/frameworks/edge/`      | n/a                                    | n/a                                  |
| Native binaries     | `/opt/frameworks/{service}/` | `/usr/local/opt/frameworks/{service}/` | `~/.local/opt/frameworks/{service}/` |
| Configuration       | `/etc/frameworks/`           | `/usr/local/etc/frameworks/`           | `~/.config/frameworks/`              |
| TLS certificates    | `/etc/frameworks/certs/`     | `/usr/local/etc/frameworks/certs/`     | `~/.config/frameworks/certs/`        |
| Internal CA bundle  | `/etc/frameworks/pki/ca.crt` | `/usr/local/etc/frameworks/pki/ca.crt` | `~/.config/frameworks/pki/ca.crt`    |
| Logs                | `/var/log/frameworks/`       | `/usr/local/var/log/frameworks/`       | `~/.local/var/log/frameworks/`       |
| Service definitions | `/etc/systemd/system/`       | `/Library/LaunchDaemons/`              | `~/Library/LaunchAgents/`            |
| Caddy data          | Docker volumes or `/var/lib` | managed by native Caddy                | managed by native Caddy              |

macOS system-domain paths follow Homebrew conventions (`/usr/local/` prefix). Local `frameworks edge deploy` without `--ssh`, and lower-level `frameworks edge provision --local`, use the user-domain paths and do not require admin privileges.

### Service Management

| Action  | Linux (systemd)                         | macOS (launchd)                                                              |
| ------- | --------------------------------------- | ---------------------------------------------------------------------------- |
| Start   | `systemctl start frameworks-helmsman`   | `launchctl kickstart system/com.livepeer.frameworks.helmsman`                |
| Stop    | `systemctl stop frameworks-helmsman`    | `launchctl kill SIGTERM system/com.livepeer.frameworks.helmsman`             |
| Restart | `systemctl restart frameworks-helmsman` | `launchctl kickstart -k system/com.livepeer.frameworks.helmsman`             |
| Enable  | `systemctl enable frameworks-helmsman`  | `launchctl bootstrap system <plist>`                                         |
| Status  | `systemctl status frameworks-helmsman`  | `launchctl print system/com.livepeer.frameworks.helmsman`                    |
| Logs    | `journalctl -u frameworks-helmsman`     | `tail -f /usr/local/var/log/frameworks/com.livepeer.frameworks.helmsman.log` |

For local user-domain launchd, replace `system/` with `gui/$(id -u)/` and use `~/.local/var/log/frameworks`. launchd plists use `RunAtLoad: true` and `KeepAlive: true`; the role loads and starts them during provisioning.

### Preflight Checks

| Check             | Linux                                                                       | macOS                                                                  |
| ----------------- | --------------------------------------------------------------------------- | ---------------------------------------------------------------------- |
| Service manager   | `systemctl` in PATH                                                         | `launchctl` in PATH                                                    |
| Sysctl tuning     | `/proc/sys/net/core/*`                                                      | Skipped                                                                |
| Shared memory     | `/dev/shm` mounted                                                          | Skipped (macOS uses different IPC)                                     |
| Port availability | `ss`/Docker checks for remote provision; Go `net.Dialer` in local preflight | `lsof` for remote/native provision; Go `net.Dialer` in local preflight |
| Disk space        | `/`, `/var/lib`                                                             | `/`, `/usr/local`                                                      |

### Helmsman Edge API

Curated read-only HTTP API on Helmsman for tray app and external tooling. Auth accepts a bearer JWT or API token, forwards validation to Foghorn, and caches successful validation results with a TTL.

```
GET /api/edge/status      — operational mode, uptime, version
GET /api/edge/streams     — active streams with viewer counts, bandwidth
GET /api/edge/streams/:id — detailed stream info (codecs, clients, source)
GET /api/edge/clients     — active client connections
GET /api/edge/health      — Helmsman/MistServer reachability from the local monitor
GET /api/edge/metrics     — bandwidth, CPU, memory snapshot
```

Data comes from Helmsman's in-memory state (already tracked from polling and triggers) plus selective MistServer API queries for client-level detail. No MistServer setters exposed.

## Key Files

- `cli/cmd/edge_deploy.go` — one-command Bridge-backed deploy flow
- `cli/cmd/cluster_nodes.go` — cluster-level node add/list/drain/fence commands
- `cli/cmd/edge.go` — lower-level provision/init/status/doctor/mode commands
- `cli/pkg/provisioner/edge.go` — `EdgeProvisioner`, preflight, HTTPS validation, detect
- `cli/pkg/provisioner/edge_role.go` — Ansible role handoff and native binary pin resolution
- `cli/internal/templates/edge.go` — manual docker-compose/Caddy/.edge.env templates
- `ansible/collections/ansible_collections/frameworks/infra/roles/edge/` — docker/native edge role
- `cli/internal/preflight/preflight.go` — `HasServiceManager()`, OS-aware checks

## Gotchas

- `ss` doesn't exist on macOS. Remote/native provisioning uses `lsof` there; local `edge preflight` uses Go checks.
- MistServer disables shared memory on darwin automatically (`meson.build` line 62).
- The .pkg installs CLI + tray app, not the edge stack. Edge deployment is done through `frameworks edge deploy`; `frameworks edge provision` is the lower-level/manual entry point.
- Native edge provisioning requires `--version` so the release manifest can resolve binary pins.
- launchd service labels: `com.livepeer.frameworks.{helmsman,caddy,mistserver}`. The uninstaller and provisioner must agree on these names.

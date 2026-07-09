# Edge Deployment - Cross-Platform Edge Node Provisioning

The edge stack (Helmsman, MistServer, Caddy) supports Linux and macOS in two modes: **container** (the single `frameworks-edge` image, all three services under s6-overlay in one container) and **native** (systemd on Linux, launchd on macOS). `docker` is accepted as a deprecated alias for container; the old 3-container compose stack is gone. The CLI detects the target OS/arch over SSH and uses the appropriate service manager and filesystem layout.

## Architecture

```
frameworks edge deploy --ssh admin@target --mode container
    │
    ├─ Bridge creates or resolves the edge cluster + enrollment token
    ├─ Bridge validates bootstrap state through Quartermaster
    ├─ PreRegisterEdge resolves node ID, edge domain, pool domain, Foghorn address
    ├─ SSH connect, detect OS/arch
    │   ├─ container → single edge image (linux: host networking; darwin: published ports)
    │   ├─ Linux + native → systemd path
    │   └─ Darwin + native → launchd path
    │
    ├─ Preflight checks (OS-aware)
    ├─ Install compose file + env (container) or pinned native binaries (native)
    ├─ Write config, internal CA, certs, and service definitions
    ├─ Start services
    └─ Helmsman enrolls with Foghorn through the control stream
```

## Container Mode (single edge image)

One image — `livepeerframeworks/frameworks-edge` (also on GHCR) — runs the whole stack under **s6-overlay v3** as PID 1: `init-seed` (oneshot) → `caddy` + `mistserver` → `helmsman`. Sources live in `edge/` (Dockerfile, s6 service tree, `stage-dist.sh`).

- **Same artifacts as native.** The image is debian-based and ships the pinned **native release tarballs** (glibc) staged by `edge/stage-dist.sh`. Foghorn-driven in-place updates (`DesiredStateUpdate`) therefore download byte-identical artifacts inside the container; `DEPLOY_MODE=container` nodes are fully eligible for automatic release convergence.
- **Persistent volumes**: `frameworks_opt:/opt/frameworks` (binaries; updates survive recreate), `frameworks_etc:/etc/frameworks` (certs/pki/mist config/version files), `caddy_etc:/etc/caddy` (activated Caddyfile), `caddy_data:/var/lib/caddy`, `edge_storage:/data/storage` (hot storage). Host binds: `./pki` (initial CA) and `./telemetry`.
- **Seed semantics** (`helmsman seed-edge`, `api_sidecar/internal/edgeseed`): installs image-baked components onto the volume when missing, and upgrades only versions a previous image seeded (tracked in `/etc/frameworks/image-seeded-versions.env`). A Foghorn-pushed version — newer or deliberately pinned older — is never touched; the release reconciler stays the source of truth.
- **Process control**: the updater's `ServiceController` seam (`api_sidecar/internal/updater/procctl*.go`) selects systemd/launchd/s6 via `HELMSMAN_SUPERVISOR` or auto-detection. Under s6: Caddy restart = admin-API `/stop` + s6 relaunch (ambient `CAP_NET_BIND_SERVICE` re-granted by the run script via `setpriv`); Mist reload = direct same-uid `SIGUSR1`; helmsman self-update = `os.Exit(0)` + s6 relaunch.
- **Users**: native parity — `frameworks` (1001) runs helmsman + Mist, `caddy` runs the proxy, `frameworks` is in the `caddy` group for the 0640 cert/key contract. The admin socket is `unix//run/caddy/admin.sock|0660`.
- **Networking**:
  - Linux: `network_mode: host`. Media UDP at native speed, loopback semantics, and the host `node_tuning` sysctls (rmem/wmem max, BBR, somaxconn) apply directly to container traffic.
  - macOS (Docker Desktop/OrbStack): bridge with the bounded published port set — 80, 443, 1935 (RTMP), 4200 (DTSC), 5554 (RTSP), 8080 (HTTP), 8889/udp (SRT), 18203/udp (WebRTC). Mist pins WebRTC and SRT to those single UDP ports in `managedProtocolDefinitions`. Docker Desktop's host networking is deliberately NOT used (broken UDP semantics on macOS). A privileged `edge-tuning` oneshot applies the non-namespaced media sysctls inside the Docker Linux VM on every `compose up`.
- **Thumbnails** need no plumbing (same-container `/tmp/mist_thumbs`); hot-storage cleanup/eviction behaves exactly as native given the dedicated `edge_storage` volume — on macOS set `HELMSMAN_STORAGE_CAPACITY_BYTES` so thresholds track a real budget instead of the whole Docker VM disk.
- **Volumes must be local-filesystem**: the atomic Mist binary swap uses `RENAME_EXCHANGE`, which fails on NFS/FUSE mounts. `seed-edge` probes this at startup and logs a hard warning.

`frameworks edge deploy` is the operator-friendly path. It can either use the logged-in Bridge flow to create/reuse an edge cluster and issue an enrollment token, or accept a pre-existing `--enrollment-token`. `frameworks edge provision` remains the lower-level/admin path for explicit domains, manifests, registration, and certificate fetches.

## Cluster Node Lifecycle

`frameworks cluster nodes ...` is the platform-operator lifecycle surface for edge nodes registered in the FrameWorks control plane:

```
frameworks cluster nodes add --ssh ubuntu@edge-1
frameworks cluster nodes list
frameworks cluster nodes drain --node edge-1
frameworks cluster nodes resume --node edge-1
frameworks cluster nodes remove --node edge-1 --wait 4h
frameworks cluster nodes evict --node edge-1
```

`cluster nodes add` always targets an existing platform-managed cluster. It defaults to the active context `cluster_id`, prompts for a cluster on TTYs when the context has no default, mints a short-lived enrollment token through Quartermaster, and passes that token directly into the existing edge deployment pipeline without printing it. Platform/provider contexts mint with GitOps-backed service auth and the system tenant. Quartermaster requires provider authority to mint node enrollment tokens; ordinary tenant subscriptions can inspect and use cluster capacity but cannot add infrastructure. Lifecycle-managed adds default to native mode and, unless `--version` is supplied, install from the cluster's release target. Clusters without a release target use the stable manifest. Container mode is a first-class install mode with the same release-target convergence: the single edge image applies in-place component updates exactly like native nodes. This keeps token handling internal while reusing the established Bridge bootstrap, Quartermaster, Foghorn, and Ansible role path.

Before provisioning, `add` probes the target over SSH. New installs write a `CLUSTER_ID` marker into the edge environment; subsequent runs use that marker to distinguish a clean target, a complete same-cluster install, a foreign cluster, and an existing edge install without a cluster marker. A same-cluster target only short-circuits when the edge stack and node marker are present, Quartermaster still has an active node registration, and Foghorn reports live node health. Partial installs, stale markers, missing registrations, non-active registry status, and unmarked installs require `--force-reapply` after operator confirmation. Foreign clusters are refused.

Node-targeted commands accept `--node <name-or-id>` and use an interactive node picker on TTYs when no selector is provided. `--node-id` remains as a deprecated scripting alias.

`drain` sets the node to `draining`, which stops new placement while allowing existing sessions to finish. `resume` returns the node to `normal`. `remove` is graceful by default: it sets `draining`, waits up to 4 hours for active streams to reach zero unless `--wait` changes the deadline, then sets `maintenance` and marks the Quartermaster registry row `retired`. `remove --wait 0` is rejected; use `evict` for immediate fencing. `evict` immediately sets `maintenance` and marks the registry row `evicted`. Mesh and DNS eligibility already filter to active nodes, so these non-active statuses remove the node from normal routing surfaces without deleting historical identity data.

## Personas

The CLI shape is shared across operator personas, but the auth path is different:

| Persona    | Typical scope                                                            | Node lifecycle auth path                                                           |
| ---------- | ------------------------------------------------------------------------ | ---------------------------------------------------------------------------------- |
| platform   | Provider/operator access to platform-official clusters and core services | GitOps-backed service token; lifecycle RPCs intentionally omit user JWT metadata   |
| selfhosted | Tenant-owned BYO edge footprint                                          | Edge deploy goes through Bridge; no direct Quartermaster/Foghorn lifecycle surface |
| user       | Account, billing, insights, Skipper interactions                         | Public Bridge/account APIs; no cluster node lifecycle mutation                     |

Self-hosted is not a separate node type from edge. It is the tenant-owned operator persona for a BYO edge footprint. The operator does not run Quartermaster or Foghorn in this flow. Bridge creates or reuses the private edge cluster, mints the enrollment token, validates bootstrap state through Quartermaster, and may proxy only Foghorn's public `PreRegisterEdge` bootstrap RPC. Those edges are still managed by the platform control plane. `frameworks cluster nodes ...` is a platform/operator surface for direct Quartermaster/Foghorn lifecycle operations, not the BYO edge path. Hosted user contexts remain separate: they can inspect account and cluster insights through public account APIs, but they cannot drain, remove, evict, or add infrastructure nodes.

## Edge Release State

Quartermaster owns the release catalog and cluster target:

- `quartermaster.edge_releases` stores release-track/version rows with the per-component release manifest. Valid tracks are `stable` and `rc`; `edge` is the component family, not a track.
- `quartermaster.cluster_release_targets` stores the target track/version, pause/resume state, and operational rollout plan for each cluster.

Foghorn owns runtime state:

- `foghorn.node_components` stores component versions reported by Helmsman in `NodeLifecycleUpdate`.
- `foghorn.node_update_state` stores the latest node update phase and error state.

Helmsman reports installed component versions on every lifecycle update. Initial native installs pass Helmsman, MistServer, Caddy, and config-schema versions into the Helmsman environment from the same GitOps manifest that supplied the artifact pins; after an agent-pull update, Helmsman records the applied component version in `/etc/frameworks/component-versions.env` (or the platform-equivalent config directory). Foghorn persists those versions and includes them in node health, so `frameworks cluster nodes list --health` can show component versions next to mode and stream counts.

The Helmsman control stream also carries `DesiredStateUpdate` and `UpdateApplyResult`. Helmsman downloads and checksum-verifies requested artifacts, applies them to the native install path, restarts/reloads the affected component, records the installed version, and rejects drain-required updates unless Foghorn supplied a non-expired cordon token. Foghorn persists component-level results and rejects apply results whose `target_release` no longer matches the node's persisted update target. Successful Mist updates move through `warming`; only after the node reports the expected component versions, sends a fresh healthy lifecycle heartbeat, and passes the edge endpoint probe does Foghorn return it to `normal` mode and mark the update phase `idle`. Failed results mark the node `failed` and leave it fenced for operator inspection.

Foghorn runs an edge release reconciler against Quartermaster's `cluster_release_targets`. It resolves the target release row, diffs **per-component** desired versions against `foghorn.node_components`, and pushes direct Helmsman/Caddy updates over the existing Helmsman stream. The diff is per-component, not row-level: a new release whose row-level `EdgeRelease.Version` advances but whose per-component `service_version` strings are unchanged (because those components carried forward — see `docs/architecture/build-and-packaging.md`) does not roll any edge node.

`service_version` in the release manifest is **artefact provenance** — which release actually produced the component's bytes — and may differ from `platform_version` per component. A `v0.2.40` release manifest can legitimately list `helmsman` with `service_version: v0.2.37` when helmsman's source was unchanged since v0.2.37; that older string flows through into `quartermaster.edge_releases.components_json.helmsman.version` and matches what edge nodes already report in `foghorn.node_components.current_version`, so reconciliation correctly decides "no-op." Config-schema versions are reported for visibility and compatibility checks; runtime configuration changes still flow through the existing ConfigSeed path, not release artifact convergence. Provider provision and `cluster upgrade --all` runs publish the selected GitOps release manifest into `quartermaster.edge_releases` and sync every manifest cluster to the selected track/version. `frameworks cluster releases publish` and `frameworks cluster releases target set` are platform repair/override commands, not the normal release path.
Release rows must include at least one updateable native edge component (`helmsman`, `mist`, or `caddy`); config-schema-only rows are rejected because they do not drive runtime convergence.
Rollout-plan JSON only accepts controls that are implemented by the current reconciler, such as canary, batch size, and error-abort limits. Capacity-floor fields are rejected until disruptive drain rollouts need them.

MistServer is special because most runtime work is process-per-session. A new Mist release can rebuild every binary and still not require a node drain. The default Mist apply strategy is therefore `rolling_stage`: Helmsman downloads and verifies the complete Mist bundle, replaces all Mist binaries atomically so there is no half-updated install state, and sends `USR1` to MistController. Existing ingest/viewer/process instances keep running; new sessions use the replaced binaries.

Foghorn should only enter cordon/drain/warm for Mist when the release manifest carries an explicit machine-generated update contract that requires it. That contract must come from the MistServer release pipeline, not operator CLI metadata and not checksum diffs. The `Livepeer-FrameWorks/mistserver` workflow currently publishes native tarballs and Docker digests; the next required release-pipeline change is to publish a Mist update contract with each release and have the monorepo GitOps manifest import it with the `external_dependencies.mistserver` entry.

## Service Responsibilities

| Component                | Role                                                                    | Data                                             |
| ------------------------ | ----------------------------------------------------------------------- | ------------------------------------------------ |
| Helmsman (`api_sidecar`) | Edge orchestrator, MistServer trigger forwarder, gRPC stream to Foghorn | Operational state, stream counts, trigger events |
| MistServer               | Media server, RTMP/E-RTMP ingest, HLS/DASH output, transcoding          | Stream data, client connections, codec info      |
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
- `cli/internal/templates/edge.go` — manual compose/.edge.env templates (container + native)
- `edge/` — single edge image: Dockerfile, s6-rc service tree, `stage-dist.sh`
- `api_sidecar/internal/edgeseed/` — container init-seed (dirs, binary install, bootstrap config)
- `api_sidecar/internal/updater/procctl*.go` — systemd/launchd/s6 service-control seam
- `ansible/collections/ansible_collections/frameworks/infra/roles/edge/` — container/native edge role
- `cli/internal/preflight/preflight.go` — `HasServiceManager()`, OS-aware checks

## Gotchas

- `ss` doesn't exist on macOS. Remote/native provisioning uses `lsof` there; local `edge preflight` uses Go checks.
- MistServer disables shared memory on darwin automatically (`meson.build` line 62).
- The .pkg installs CLI + tray app, not the edge stack. Edge deployment is done through `frameworks edge deploy`; `frameworks edge provision` is the lower-level/manual entry point.
- Edge provisioning pins release artifacts in both modes (native binary pins, container image digest). Without `--version` it defaults to the stable release channel.
- launchd service labels: `com.livepeer.frameworks.{helmsman,caddy,mistserver}`. The uninstaller and provisioner must agree on these names.
- Container-mode `frameworks edge logs caddy|mistserver|helmsman` all map to the single `edge` container's interleaved stdout.
- Management commands (`edge status/update/logs/doctor/drift`) resolve the compose project automatically: `./docker-compose.edge.yml` (from `edge init`) or `/opt/frameworks/edge/docker-compose.yml` (Ansible-provisioned). `--dir` overrides.
- Remote macOS container deploys require Docker Desktop with "Allow the default Docker socket to be used" enabled (or OrbStack): the Ansible play escalates, and root must reach the daemon. The role probes `docker info` and fails with that guidance.
- The edge image bakes the helmsman version the release manifest advertises — including carry-forward releases, where the image job downloads the carried tarball from the baseline release instead of expecting a fresh build.
- The edge container's `/opt/frameworks` and `/data/storage` volumes must be local filesystems (RENAME_EXCHANGE + honest `Statfs`); never NFS/FUSE binds. On macOS prefer named volumes over virtiofs binds for IO speed.

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

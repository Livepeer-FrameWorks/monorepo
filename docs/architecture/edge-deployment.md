# Edge Deployment - Cross-Platform Edge Node Provisioning

The edge stack (Helmsman, MistServer, Caddy) runs on Linux and macOS. The CLI detects the target OS via SSH (`uname -s`) and uses the appropriate service manager and filesystem layout.

## Architecture

```
frameworks edge provision --mode native --host admin@target
    │
    ├─ SSH connect, detect OS
    │   ├─ Linux → systemd path
    │   └─ Darwin → launchd path
    │
    ├─ Preflight checks (OS-aware)
    ├─ Download signed binaries from release manifest
    ├─ Install binaries + config + certs
    ├─ Generate service definitions (unit files / plists)
    ├─ Start services
    └─ Enroll with platform (Foghorn)
```

## Service Responsibilities

| Component                | Role                                                                    | Data                                             |
| ------------------------ | ----------------------------------------------------------------------- | ------------------------------------------------ |
| Helmsman (`api_sidecar`) | Edge orchestrator, MistServer trigger forwarder, gRPC stream to Foghorn | Operational state, stream counts, trigger events |
| MistServer               | Media server, RTMP ingest, HLS/DASH output, transcoding                 | Stream data, client connections, codec info      |
| Caddy                    | TLS termination, reverse proxy, ACME cert management                    | Certificates, access logs                        |

## Platform Differences

### Filesystem Layout

| Purpose             | Linux                        | macOS                                  |
| ------------------- | ---------------------------- | -------------------------------------- |
| Service binaries    | `/opt/frameworks/{service}/` | `/usr/local/opt/frameworks/{service}/` |
| Configuration       | `/etc/frameworks/`           | `/usr/local/etc/frameworks/`           |
| TLS certificates    | `/etc/frameworks/certs/`     | `/usr/local/etc/frameworks/certs/`     |
| Logs                | `/var/log/frameworks/`       | `/usr/local/var/log/frameworks/`       |
| Service definitions | `/etc/systemd/system/`       | `/Library/LaunchDaemons/`              |
| Caddy data          | `/var/lib/caddy/`            | `/usr/local/var/lib/caddy/`            |

macOS paths follow Homebrew conventions (`/usr/local/` prefix).

### Service Management

| Action  | Linux (systemd)                         | macOS (launchd)                                                              |
| ------- | --------------------------------------- | ---------------------------------------------------------------------------- |
| Start   | `systemctl start frameworks-helmsman`   | `launchctl kickstart system/com.livepeer.frameworks.helmsman`                |
| Stop    | `systemctl stop frameworks-helmsman`    | `launchctl kill SIGTERM system/com.livepeer.frameworks.helmsman`             |
| Restart | `systemctl restart frameworks-helmsman` | `launchctl kickstart -k system/com.livepeer.frameworks.helmsman`             |
| Enable  | `systemctl enable frameworks-helmsman`  | `launchctl bootstrap system <plist>`                                         |
| Status  | `systemctl status frameworks-helmsman`  | `launchctl print system/com.livepeer.frameworks.helmsman`                    |
| Logs    | `journalctl -u frameworks-helmsman`     | `tail -f /usr/local/var/log/frameworks/com.livepeer.frameworks.helmsman.log` |

launchd plists use `RunAtLoad: false` — services are loaded but not started until the user configures the env file and kicks them (or the CLI does it during provisioning).

### Preflight Checks

| Check             | Linux                  | macOS                              |
| ----------------- | ---------------------- | ---------------------------------- |
| Service manager   | `systemctl` in PATH    | `launchctl` in PATH                |
| Sysctl tuning     | `/proc/sys/net/core/*` | Skipped                            |
| Shared memory     | `/dev/shm` mounted     | Skipped (macOS uses different IPC) |
| Port availability | Go `net.Dialer`        | Go `net.Dialer`                    |
| Disk space        | `/`, `/var/lib`        | `/`, `/usr/local`                  |

### Helmsman Edge API (planned)

Curated read-only HTTP API on Helmsman for tray app and external tooling. Auth via JWT validated through Foghorn (cached).

```
GET /api/edge/status      — operational mode, uptime, version
GET /api/edge/streams     — active streams with viewer counts, bandwidth
GET /api/edge/streams/:id — detailed stream info (codecs, clients, source)
GET /api/edge/clients     — active client connections
GET /api/edge/health      — service health (Helmsman, MistServer, Caddy)
GET /api/edge/metrics     — bandwidth, CPU, memory snapshot
GET /api/edge/logs        — recent log entries
```

Data comes from Helmsman's in-memory state (already tracked from polling and triggers) plus selective MistServer API queries for client-level detail. No MistServer setters exposed.

## Key Files

- `cli/pkg/provisioner/edge.go` — `installNativeDarwin()`, macOS path constants (~line 501)
- `cli/pkg/provisioner/templates.go` — `GenerateLaunchdPlist()` plist XML generation
- `cli/internal/preflight/preflight.go` — `HasServiceManager()`, OS-aware checks
- `cli/cmd/edge.go` — `newEdgePreflightCmd()`, `newEdgeDoctorCmd()`

## Gotchas

- `ss` doesn't exist on macOS. Port checks use Go's `net.Dialer`, not shell commands.
- MistServer disables shared memory on darwin automatically (`meson.build` line 62).
- The .pkg installs CLI + tray app, not the edge stack. Edge provisioning is always done through `frameworks edge provision`.
- launchd service labels: `com.livepeer.frameworks.{helmsman,caddy,mistserver}`. The uninstaller and provisioner must agree on these names.

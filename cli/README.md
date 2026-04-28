# FrameWorks CLI

Unified operator tool for managing FrameWorks contexts, credentials, edge nodes, central and regional clusters, mesh networking, DNS checks, service discovery, admin operations, and Livepeer gateway operations.

## Command Groups

- `frameworks setup`, `context`, `login`, `logout`, and `menu` manage local operator state and interactive setup.
- `frameworks cluster ...` handles cluster preflight, provisioning, initialization, status, drift checks, backups, restores, migrations, seed data, upgrades, logs, restarts, diagnostics, GeoIP sync, and release-channel changes.
- `frameworks edge ...` handles edge deploy, preflight, tuning, initialization, enrollment, provisioning, status, updates, certificates, logs, diagnostics, and node mode changes.
- `frameworks mesh ...` handles mesh status, WireGuard identity operations, runtime joins, reconciliation, and diagnostics.
- `frameworks services ...` plans, starts, stops, inspects, and checks local central-service stacks.
- `frameworks dns doctor` validates DNS wiring.
- `frameworks livepeer ...`, `admin ...`, and `config env` cover Livepeer gateway operations, admin helpers, and environment rendering.

## Build and Test

From the repository root:

```bash
make build-bin-cli
make test-cli
```

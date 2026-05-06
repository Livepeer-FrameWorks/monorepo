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

## Personas and Access Modes

`frameworks setup` saves a persona and an access mode in each context:

- `platform` is for operators who manage the full FrameWorks platform. Platform setup stores Bridge as the public API URL, stores GitOps manifest defaults, and defaults control-plane gRPC access to `ssh`. Commands such as `admin`, `services`, `dns`, and live mesh inspection resolve core service endpoints from the manifest and open SSH local-forwards to the hosts that run those services.
- `selfhosted` is for tenant operators who run BYO edge nodes through Bridge. It does not imply direct access to Quartermaster, Foghorn, or other platform core services.
- `user` is for hosted account/API usage: login, insights, Skipper, and account-facing workflows through public Bridge. This is the persona for someone who is not running their own edge node.

Older configs that still say `persona: edge` are normalized to `user`; `edge` is not a user-facing persona.

Access modes:

- `local`: use saved endpoint fields in the context.
- `ssh`: for platform contexts, load the GitOps manifest (including `hosts_file`) and tunnel to the service host over SSH.
- `mesh`: for platform contexts, dial the manifest WireGuard address directly. Use this only from a machine already on the mesh.

Change a context with:

```bash
frameworks context set-access-mode <local|ssh|mesh>
```

## Build and Test

From the repository root:

```bash
make build-bin-cli
make test-cli
```

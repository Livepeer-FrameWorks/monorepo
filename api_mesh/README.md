# Privateer (WireGuard Mesh Agent)

Status: **In Testing**

Lightweight agent that automates WireGuard mesh networking across infrastructure nodes. Uses token-based authentication for secure node enrollment.

## Why Privateer?

Privateer provides mesh networking **without external SaaS dependency**:

- **No vendor lock-in**: Pure WireGuard orchestration under your control—no Tailscale, Nebula, or cloud VPN required
- **Tenant isolation**: Per-tenant network segments enable B2B deployments with isolated customer infrastructure
- **Sovereignty-first**: Critical network infrastructure remains entirely in your hands

For self-hosted deployments, Privateer ensures your nodes communicate securely without depending on third-party coordination services.

## What it does

- Manages WireGuard peer connections automatically
- Token-based join: new nodes join mesh with time-limited bootstrap tokens
- Peer discovery via Quartermaster
- Health monitoring and reporting
- Local DNS resolution for mesh hostnames (port 5353)

## How nodes join

The agent uses the bootstrap token to register the node, then uses the service token for ongoing mesh sync:

```bash
ENROLLMENT_TOKEN=bt_xxx SERVICE_TOKEN=svc_xxx privateer
```

## Run (dev)

- Start the agent directly with env vars (no interactive join/init subcommands yet).

Configuration comes from the top-level `config/env` stack. Generate `.env` with `make env` and customise `config/env/secrets.env` for secrets. Do not commit secrets.

## Required env vars

- `SERVICE_TOKEN` (required for mesh sync)
- `ENROLLMENT_TOKEN` (optional; used once to register the node)

## Optional env vars

- `MESH_NODE_TYPE` (default: `edge`)
- `MESH_NODE_NAME` (default: hostname)
- `MESH_EXTERNAL_IP` (optional; used during bootstrap)
- `MESH_INTERNAL_IP` (optional; used during bootstrap)
- `MESH_LISTEN_PORT` (default: `51820`)
- `PRIVATEER_SYNC_INTERVAL` (default: `30s`)
- `PRIVATEER_SYNC_TIMEOUT` (default: `10s`)

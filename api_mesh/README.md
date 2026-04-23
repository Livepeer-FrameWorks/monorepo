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
- Local DNS resolution for mesh hostnames (port 53)

## How nodes join

The agent starts from GitOps-rendered WireGuard identity + seed peers, then uses
the shared service token to reconcile against Quartermaster and self-register
the node row if needed:

```bash
SERVICE_TOKEN=svc_xxx QUARTERMASTER_GRPC_ADDR=10.88.0.1:19002 privateer
```

## Run (dev)

- Start the agent directly with env vars (no interactive join/init subcommands yet).

Configuration comes from the top-level `config/env` stack. Generate `.env` with `make env` and customise `config/env/secrets.env` for secrets. Do not commit secrets.

## Required env vars

- `SERVICE_TOKEN` (required)
- `QUARTERMASTER_GRPC_ADDR` (required)
- `CLUSTER_ID` (required for self-registration and internal cert token minting)

## Optional env vars

- `MESH_NODE_TYPE` (default: `core`)
- `MESH_NODE_NAME` (default: hostname)
- `MESH_EXTERNAL_IP` (optional; sent when self-registering the node row)
- `MESH_INTERNAL_IP` (optional; sent when self-registering the node row)
- `MESH_LISTEN_PORT` (default: `51820`)
- `MESH_WIREGUARD_IP` (used for startup seed/last-known apply and self-registration)
- `MESH_PRIVATE_KEY_FILE` (used for startup seed/last-known apply)
- `PRIVATEER_STATIC_PEERS_FILE` (used for startup seed apply)
- `PRIVATEER_DATA_DIR` (defaults to `"/var/lib/privateer"`)
- `PRIVATEER_SYNC_INTERVAL` (default: `30s`)
- `PRIVATEER_SYNC_TIMEOUT` (default: `10s`)

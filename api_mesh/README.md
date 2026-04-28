# Privateer (WireGuard Mesh Agent)

Status: **In Testing**

Lightweight agent that automates WireGuard mesh networking across infrastructure nodes. It supports GitOps-seeded nodes and runtime node enrollment through `frameworks mesh join`.

## Why Privateer?

Privateer provides mesh networking **without external SaaS dependency**:

- **No vendor lock-in**: Pure WireGuard orchestration under your control—no Tailscale, Nebula, or cloud VPN required
- **Cluster-scoped mesh**: Peers are reconciled per cluster through Quartermaster. Per-tenant mesh segmentation is still RFC/future work.
- **Sovereignty-first**: Critical network infrastructure remains entirely in your hands

For self-hosted deployments, Privateer ensures your nodes communicate securely without depending on third-party coordination services.

## What it does

- Manages WireGuard peer connections automatically
- Token-based join: new nodes can enroll with a bootstrap token delivered by `frameworks mesh join`
- Seed bootstrap: GitOps-rendered WireGuard identity, static peers, and static DNS can bring `wg0` up before the first control-plane sync
- Peer discovery and mesh revision sync via Quartermaster
- Internal certificate and ingress TLS bundle sync via Navigator/Quartermaster when configured
- Health/metrics HTTP server on `PRIVATEER_PORT` (default 18012)
- Local DNS resolution for `.internal` hostnames on loopback (default port 53), with optional upstream forwarding

## How nodes join

Seed-managed nodes start from GitOps-rendered WireGuard identity + seed peers, then use the shared service token to reconcile against Quartermaster:

```bash
SERVICE_TOKEN=svc_xxx QUARTERMASTER_GRPC_ADDR=10.88.0.1:19002 privateer
```

Runtime-enrolled nodes use `frameworks mesh join <ssh-target>`, which writes `/etc/privateer/privateer.env` with `SERVICE_TOKEN`, `MESH_JOIN_TOKEN`, `BRIDGE_BOOTSTRAP_ADDR`, and bootstrap hints. On first start, Privateer generates a WireGuard keypair, calls the Bridge bootstrap endpoint, persists `enrollment.json`, and then syncs normally with Quartermaster.

## Run (dev)

- Start the agent directly with env vars, or use `frameworks mesh join <ssh-target>` for runtime enrollment.

Configuration comes from the top-level `config/env` stack. Generate `.env` with `make env` and customise `config/env/secrets.env` for secrets. Do not commit secrets.

## Required env vars

- `SERVICE_TOKEN` (required)
- `QUARTERMASTER_GRPC_ADDR` (required)
- `CLUSTER_ID` (required for self-registration and internal cert token minting)
- `MESH_PRIVATE_KEY_FILE` (required)
- `MESH_WIREGUARD_IP` (required unless supplied by persisted enrollment state)

## Optional env vars

- `MESH_JOIN_TOKEN` + `BRIDGE_BOOTSTRAP_ADDR` (runtime enrollment path when no private key exists yet)
- `MESH_NODE_TYPE` (default: `core`)
- `MESH_NODE_NAME` (default: hostname)
- `MESH_EXTERNAL_IP` (optional; sent when self-registering the node row)
- `MESH_INTERNAL_IP` (optional; sent when self-registering the node row)
- `MESH_LISTEN_PORT` (default: `51820`)
- `DNS_PORT` (default: `53`)
- `UPSTREAM_DNS` (comma-separated upstream resolvers for non-`.internal` queries)
- `NAVIGATOR_GRPC_ADDR`, `CERT_ISSUANCE_TOKEN`, `GRPC_TLS_PKI_DIR`, `EXPECTED_INTERNAL_GRPC_SERVICES`
- `PRIVATEER_STATIC_PEERS_FILE` (used for startup seed apply)
- `PRIVATEER_DATA_DIR` (defaults to `"/var/lib/privateer"`)
- `PRIVATEER_SYNC_INTERVAL` (default: `30s`)
- `PRIVATEER_SYNC_TIMEOUT` (default: `10s`)
- `PRIVATEER_CERT_SYNC_INTERVAL` (default: `5m`)

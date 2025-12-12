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
- Token-based join: new nodes join mesh with time-limited JWT tokens
- Peer discovery via Quartermaster
- Health monitoring and reporting
- Local DNS resolution for mesh hostnames (port 5353)

## How nodes join
```bash
# Admin generates token via Quartermaster
# New node joins with single command
privateer join --token=<signed-jwt-token>
```

The agent validates the token, generates WireGuard keys, connects to the bootstrap peer, registers with Quartermaster, and establishes mesh connections.

## Run (dev)
- Bootstrap node: `privateer init --role=bootstrap --listen=0.0.0.0:51820`
- Regular node: `privateer join --token=<token>`

Configuration comes from the top-level `config/env` stack. Generate `.env` with `make env` and customise `config/env/secrets.env` for secrets. Do not commit secrets.

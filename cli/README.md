# FrameWorks CLI

Unified operator tool for managing the FrameWorks platform: contexts/connectivity, edge stack, and central services (planned).

## What It Does (current)

- **Contexts & Reachability**: Manage endpoints and check HTTP(/health) and gRPC health with optional JSON output.
- **Edge Templates**: Generate `.edge.env`, `docker-compose.edge.yml`, and `Caddyfile` using the active context.
- **Edge Preflight**: Verify DNS (optional), Docker/compose, Linux sysctls, `/dev/shm`, `ulimit`, and ports 80/443.
- **Edge Tune**: Dry‑run or apply recommended sysctl/limits (root needed for system paths).
- **Env Generation**: `frameworks config env generate` merges `config/env` layers into ready-to-use `.env` files (dev/prod/targets).

Planned (tracked in SCOPE.md): workspace mode and optional interface helpers.

## Installation

```bash
# Download latest release (choose your platform)
# Linux (amd64)
curl -L https://github.com/Livepeer-FrameWorks/monorepo/releases/latest/download/frameworks-linux-amd64 -o frameworks
# Linux (arm64)
# curl -L https://github.com/Livepeer-FrameWorks/monorepo/releases/latest/download/frameworks-linux-arm64 -o frameworks
# macOS (Apple Silicon)
# curl -L https://github.com/Livepeer-FrameWorks/monorepo/releases/latest/download/frameworks-darwin-arm64 -o frameworks
# macOS (Intel)
# curl -L https://github.com/Livepeer-FrameWorks/monorepo/releases/latest/download/frameworks-darwin-amd64 -o frameworks

chmod +x frameworks
sudo mv frameworks /usr/local/bin/

# Verify installation
frameworks version
```

## Quick Start

### Set up a local context and validate connectivity

```bash
# Initialize config with localhost defaults
frameworks context init

# Check reachability (HTTP/gRPC); add --output json for machine‑readable
frameworks context check --timeout 2s
```

### Prepare an edge host

```bash
# Generate edge stack templates (manual DNS expected)
frameworks edge init --dir . \
  --domain stream.example.com \
  --email ops@example.com

# Run preflight checks (DNS, Docker, sysctls, ports)
frameworks edge preflight --domain stream.example.com

# Preview and optionally apply recommended tuning
frameworks edge tune           # writes preview files in cwd
sudo frameworks edge tune --write  # writes to /etc/sysctl.d and /etc/security/limits.d
```

## Commands (today)

```
frameworks
├── context         # Endpoints, executor, and reachability
│   ├── init        # Create default local context
│   ├── list        # List contexts
│   ├── use         # Switch current context
│   ├── show        # Display context details
│   ├── set-url     # Update a service URL
│   └── check       # HTTP(/health) + gRPC health checks
│
├── edge            # Edge node lifecycle
│   ├── init        # Write .edge.env, docker-compose.edge.yml, Caddyfile
│   ├── preflight   # Host checks (DNS/Docker/sysctls/ports)
│   ├── tune        # Apply recommended sysctl/limits
│   ├── enroll      # Start stack & enroll (compose up + HTTPS readiness)
│   ├── status      # Local container status + HTTPS check
│   ├── update      # Pull & restart
│   ├── logs        # Tail logs
│   └── cert        # Show TLS expiry; --reload to reload Caddy
│
├── services        # Central-tier ops
│   ├── plan        # Generate per-service compose fragments (+ plan.yaml)
│   ├── up          # Start services (merges svc-*.yml; supports --only, --ssh)
│   ├── down        # Stop services (supports --only, --ssh)
│   ├── status      # Container status (supports --only, --ssh)
│   ├── logs        # Logs (supports --only, --follow, --tail, --ssh)
│   ├── health      # Aggregated service health (Quartermaster)
│   └── discover    # Service discovery (Quartermaster)
│
├── config          # Configuration helpers
│   └── env         # Merge config/env layers into an env file (CLI reuse of configgen)
│
├── dns             # CloudFlare DNS & Load Balancer automation
│   ├── create-pool         # Create load balancer pool
│   ├── list-pools          # List all pools
│   ├── delete-pool         # Delete pool
│   ├── add-origin          # Add origin to pool
│   ├── remove-origin       # Remove origin from pool
│   ├── create-subdomain    # Create DNS subdomain (A/CNAME)
│   ├── list-subdomains     # List DNS records
│   ├── delete-subdomain    # Delete DNS record
│   ├── health-check        # Configure health monitors
│   ├── create-lb           # Create geo-routed load balancer
│   └── list-lbs            # List load balancers
│
├── login           # Store JWT/service token in context
├── admin           # Provider/admin operations
│   ├── tokens              # Developer API tokens via Gateway (create/list/revoke)
│   └── bootstrap-tokens    # Quartermaster bootstrap tokens (create/list/revoke)
└── version         # Print CLI and platform version
```

## What's Coming

**Next Up**
- Workspace mode for local builds
- Interfaces (webapp/marketing): dev helpers first; optional deploy later

## Versioning

- Platform SemVer: one tag (e.g., `v1.4.0`) defines the stack.
- Release Manifest: assembled by the GitHub Actions `manifest` job and attached to each GitHub Release (also mirrored to the GitOps repo as `releases/<version>.yaml`), listing images, digests, binaries, and interfaces.
- Build Info: all binaries embed Version/GitCommit/BuildDate; `frameworks version` prints platform info.

## Configuration

The CLI uses contexts for different environments:

```bash
# Switch to a context
frameworks context use local

# Show current context details
frameworks context show
```

Authentication methods:
- JWT: `frameworks login --email you@example.com` stores a user token in the current context.
- Service token: `frameworks login --service-token <TOKEN>` stores a provider token in the current context.

### DNS Management

The CLI includes CloudFlare DNS automation for managing load balancer pools and geo-routing:

```bash
# Set CloudFlare credentials (in shell or config/env/secrets.env)
export CLOUDFLARE_API_TOKEN="your-token"
export CLOUDFLARE_ZONE_ID="your-zone-id"
export CLOUDFLARE_ACCOUNT_ID="your-account-id"

# Or add to config/env/secrets.env and run: make env

# Create a pool
frameworks dns create-pool us-east-pool --description="US East origins"

# Add origins
frameworks dns add-origin <pool-id> --address=192.0.2.10 --name=edge-1

# Configure health checks
frameworks dns health-check <pool-id> --path=/health --port=443

# Create tenant subdomain
frameworks dns create-subdomain tenant1 --target=play.example.com
```

See the [DNS documentation](../website_docs/src/content/docs/operators/dns.mdx) for complete details.

## System Requirements

**Edge Nodes:**
- Docker & Docker Compose
- 4+ CPU cores, 8GB+ RAM
- Public IP with ports 80, 443, 1935, 8080
- DNS A record pointing to node

**Control Host** (running CLI):
- Network access to target nodes (local or SSH)
- Optional: Access to Quartermaster for enrollment

## Troubleshooting

Tips:
- Use `frameworks context check --timeout 2s` to quickly validate connectivity.
- Run `frameworks edge preflight --domain <EDGE_DOMAIN>` before deploying the edge stack.
- Use `frameworks edge tune --write` once you approve the preview files.
- `frameworks edge doctor` combines host checks, compose status, and HTTPS health.

## Development

Building from source:

```bash
git clone <this monorepo>
cd cli
go build -o frameworks .
```

## Notes

- The CLI is self‑documenting — use `--help` on any command
- Most operations can run locally or via SSH in the future (tracked)
- Edge nodes operate independently without direct Quartermaster access
- Many commands support `--output json` for scripting

## Support

For internal use while under active development.

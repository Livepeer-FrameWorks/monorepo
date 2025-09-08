# Privateer (WireGuard Mesh Agent)

> **Status**: 🚧 **Planned** - High priority infrastructure agent

## Overview

Privateer is a lightweight agent that automates WireGuard mesh networking across FrameWorks infrastructure. Each node runs a Privateer agent that connects to a secure mesh network using token-based authentication.

## Architecture

### The Bootstrap Problem
FrameWorks services require a secure VPN mesh for inter-service communication, but something needs to establish the mesh first. Privateer solves this chicken-and-egg problem using a token-based join mechanism.

### Core Components

```
Admin → Quartermaster → Generate Join Token
                ↓
New Node → Privateer Agent → Join via Token
                ↓
Bootstrap Peer (Central Node) → Accept Connection
                ↓
Node on Mesh → Register with Quartermaster
                ↓
Full Mesh Member → Get peer list & expand connections
```

**Central Node (Bootstrap Peer)**:
- Runs Quartermaster + Privateer agent
- Acts as mesh entry point for new nodes
- Has public IP for initial connections

**Edge/Regional Nodes**:
- Run Privateer agent only
- Join mesh using time-limited tokens
- Auto-register with Quartermaster once connected

## Token-Based Join Process

### 1. Admin generates join token
```bash
# Via Quartermaster API
curl -X POST https://quartermaster.example.com/api/v1/mesh/tokens \
  -H "Authorization: Bearer $ADMIN_TOKEN" \
  -d '{"expires_in": "24h", "node_type": "edge"}'

# Returns signed JWT with:
# - Bootstrap peer endpoint
# - Bootstrap peer WireGuard public key  
# - Mesh network CIDR
# - Expiry time
```

### 2. New node joins mesh
```bash
# Single command deployment
privateer join --token=eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9...
```

**What happens:**
1. Privateer validates token signature
2. Generates local WireGuard keypair
3. Connects to bootstrap peer using token info
4. Registers with Quartermaster via mesh connection
5. Gets full peer list and establishes all connections
6. Monitors peer health and reports to Quartermaster

### 3. Ongoing operation
- Periodic peer list updates from Quartermaster
- Dynamic WireGuard config generation  
- Zero-downtime config reloads
- Health monitoring and reporting
- Automatic peer discovery for new nodes

## Deployment Scenarios

### Manual (Development/Testing)
```bash
# Central node (first time setup)
privateer init --role=bootstrap --listen=0.0.0.0:51820

# Any new node  
privateer join --token=$(get-token-from-admin)
```

### Automated (Production)
```yaml
# Ansible example
- name: Join node to mesh
  command: privateer join --token={{ mesh_join_token }}
  become: yes
```

```hcl
# Terraform example
resource "frameworks_mesh_token" "node" {
  expires_in = "1h"
  node_type  = "edge"
}

resource "compute_instance" "edge" {
  user_data = <<-EOF
    #!/bin/bash
    privateer join --token=${frameworks_mesh_token.node.token}
  EOF
}
```

## Configuration

Configuration will be provided via an `env.example` file (with inline comments) when this service is implemented. Copy it to `.env` and adjust values for your environment.

### Bootstrap Node Example
```bash
privateer init \
  --role=bootstrap \
  --listen=0.0.0.0:51820 \
  --network=10.10.0.0/16 \
  --quartermaster=http://localhost:18002
```

### Regular Node Example  
```bash
privateer join \
  --token=<signed-jwt-token> \
  --interface=wg0
```

## Security Model

### Token Security
- **Time-limited**: Tokens expire (default 24h, configurable)
- **Single-use**: Tokens are invalidated after successful join
- **Signed**: JWT tokens signed by Quartermaster private key
- **Minimal exposure**: Only bootstrap peer endpoint exposed

### Network Security
- **Encrypted**: All mesh traffic encrypted via WireGuard
- **Authenticated**: Peers authenticate via WireGuard public keys
- **Isolated**: Mesh network separated from public internet
- **Audited**: All join events logged to Quartermaster

### Bootstrap Security
- Bootstrap peer only accepts connections with valid tokens
- No permanent public API endpoints
- Bootstrap peer rotates its own WireGuard key periodically
- Failed join attempts logged and rate-limited

## Integration Points

### With Quartermaster
- **Node Registry**: Registers node info after mesh join
- **Peer Discovery**: Gets full mesh peer list
- **Health Reporting**: Reports connectivity and latency metrics
- **Token Management**: Validates join tokens

### With Other Services
- **Service Discovery**: Other services discover nodes via Quartermaster
- **Health Checks**: Provides mesh connectivity status
- **Monitoring**: Exports metrics for Prometheus

## API Endpoints (Local Agent)

- `GET /health` - Agent health and mesh status
- `GET /peers` - Current peer connections and status
- `GET /metrics` - Prometheus metrics
- `POST /reload` - Reload configuration
- `GET /info` - Agent version and config info

## Metrics

Privateer exports these metrics for monitoring:
- `privateer_peer_count` - Number of connected peers
- `privateer_peer_latency_ms` - Peer latency measurements
- `privateer_packet_loss_ratio` - Packet loss to peers
- `privateer_bytes_sent/received` - Network traffic
- `privateer_last_handshake_seconds` - Time since last WireGuard handshake

## Database Schema (via Quartermaster)

Privateer leverages Quartermaster's existing node management:

```sql
-- Quartermaster manages this table
infrastructure_nodes (
  node_id,
  cluster_id,
  wireguard_ip,
  wireguard_public_key,
  status,
  health_score,
  last_heartbeat,
  ...
)
```

Privateer updates:
- `wireguard_public_key` - during registration
- `health_score` - based on peer connectivity
- `last_heartbeat` - periodic keepalive

## Future Enhancements

- **Mesh Topology Control**: Configure which nodes peer with which
- **Traffic Shaping**: QoS for different service types
- **Key Rotation**: Automatic WireGuard key rotation
- **Split Tunneling**: Route only service traffic through mesh
- **Kubernetes Integration**: CNI plugin for pod networking

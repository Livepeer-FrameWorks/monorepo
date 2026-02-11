# RFC: Agent-Operated Edge Nodes

Status: Draft

## Problem

Agents can create streams and interact with the FrameWorks platform via MCP, but all video infrastructure (edge nodes running MistServer) is operator-managed. Agents cannot bring their own compute to the network.

This limits:

- Agents that want dedicated or geo-specific streaming infrastructure.
- Selfhosted deployment models where agents contribute compute.
- Agent autonomy over deployment, configuration, and scaling decisions.

## Goals

- Agents provision and operate their own edge nodes via MCP.
- Nodes join the FrameWorks network (WireGuard mesh, viewer routing, billing).
- Tenant isolation by default: agent nodes serve only the agent's own streams.
- Prepaid billing model applies to stream usage on agent nodes.

## Non-Goals

- Global node marketplace where agents earn credits by serving other tenants (future phase).
- Replacing operator-managed infrastructure.
- Edge-local MCP endpoints running on the node itself (future phase).
- Changes to the core billing model.

---

## Background: Current Node System

| Component     | Service          | Role                                                                    |
| ------------- | ---------------- | ----------------------------------------------------------------------- |
| Quartermaster | `api_tenants/`   | Control plane: node registration, cluster management, enrollment tokens |
| Foghorn       | `api_balancing/` | Load balancer: viewer routing, node lifecycle, operational modes        |
| Helmsman      | `api_sidecar/`   | Edge sidecar: manages MistServer, reports metrics via gRPC stream       |
| Privateer     | `api_mesh/`      | WireGuard mesh: inter-node connectivity, local DNS resolution           |
| Navigator     | `api_dns/`       | Public DNS automation, ACME certificate issuance                        |

### Current Bootstrap Flow

1. Operator creates an **enrollment token** via CLI or Quartermaster API.
2. Helmsman starts with `ENROLLMENT_TOKEN` and dials Foghorn's gRPC stream.
3. Helmsman sends a `Register` message containing node_id, capabilities, hardware specs, and a fingerprint (MAC hash + machine-id hash).
4. Quartermaster validates the token via `BootstrapInfrastructureNode()` and creates a database record in `infrastructure_nodes`.
5. Privateer joins the WireGuard mesh and starts local DNS.
6. The node begins sending `NodeLifecycleUpdate` heartbeats to Foghorn.

### Current Trust Model

- Enrollment tokens are operator-managed (single-use or multi-use with usage limits).
- Tokens can be cluster-bound and IP-restricted.
- Node identity is fingerprint-bound (MAC hash + machine-id hash) to prevent token reuse.
- Dedicated nodes carry a `tenant_id` field for tenant isolation in routing.

### Key Files

- `api_tenants/internal/grpc/server.go:2888-3054` — `BootstrapInfrastructureNode()`
- `api_sidecar/internal/control/client.go` — Helmsman ↔ Foghorn gRPC stream
- `api_balancing/internal/state/stream_state.go:124-196` — `NodeState` struct
- `api_balancing/internal/balancer/balancer.go:138-260` — Viewer routing and scoring
- `api_mesh/internal/agent/agent.go` — Privateer agent
- `cli/pkg/provisioner/privateer.go` — Node provisioning via CLI
- `pkg/proto/ipc.proto` — gRPC messages (Register, NodeLifecycleUpdate, Heartbeat)
- `pkg/proto/quartermaster.proto:762-785` — InfrastructureNode message

---

## Design

### Enrollment via MCP

A new MCP tool allows agents to generate enrollment tokens scoped to their tenant:

**`create_enrollment_token`**

| Parameter        | Required | Description                                                                    |
| ---------------- | -------- | ------------------------------------------------------------------------------ |
| `node_type`      | No       | Capabilities hint: `edge`, `ingest`, `storage`, `processing` (default: `edge`) |
| `cluster_id`     | No       | Assign to specific cluster (default: auto-assign)                              |
| `expiry_minutes` | No       | Token TTL (default: 60 minutes)                                                |

Returns:

```json
{
  "enrollment_token": "enroll_xxx",
  "expires_at": "2025-02-01T13:00:00Z",
  "provisioning": {
    "docker_compose_url": "https://releases.frameworks.network/edge-node/docker-compose.yml",
    "helmsman_binary": "https://releases.frameworks.network/helmsman/latest",
    "privateer_binary": "https://releases.frameworks.network/privateer/latest",
    "instructions": "Set ENROLLMENT_TOKEN=enroll_xxx and start services."
  }
}
```

Implementation: wraps the existing `CreateEnrollmentToken` Quartermaster RPC, setting `tenant_id` from the agent's auth context and enforcing single-use + short expiry by default.

### Node Provisioning

The agent provisions compute (bare metal, VPS, cloud instance, etc.) and either:

**Option A: Docker Compose**

```bash
ENROLLMENT_TOKEN=enroll_xxx docker compose -f docker-compose.yml up -d
```

The compose file runs Helmsman + Privateer + MistServer as a single stack.

**Option B: Binary Installation**

1. Download Helmsman and Privateer binaries from releases.
2. Configure with the enrollment token.
3. Start as systemd services.

On startup, the standard bootstrap flow runs: Helmsman dials Foghorn, Privateer joins mesh, node begins heartbeating.

### Tenant-Scoped Nodes

Agent-provisioned nodes are tenant-isolated by design:

- The enrollment token carries `tenant_id` from the agent's auth context.
- `BootstrapInfrastructureNode()` sets `tenant_id` on the `infrastructure_nodes` record.
- Foghorn routing already respects `tenant_id` on `NodeState`: tenant-scoped nodes only serve that tenant's streams.
- The node appears in the agent's existing `nodes://list` MCP resource.

No changes to the routing algorithm are needed; the `tenant_id` filter in `balancer.go` already handles this.

### New MCP Tools

| Tool                      | Description                                          | Maps To                                  |
| ------------------------- | ---------------------------------------------------- | ---------------------------------------- |
| `create_enrollment_token` | Generate token scoped to agent's tenant              | Quartermaster `CreateEnrollmentToken`    |
| `set_node_mode`           | Set operational mode (normal, draining, maintenance) | Foghorn `PUT /nodes/:id/mode`            |
| `get_node_health`         | Real-time health metrics for a specific node         | Foghorn node state lookup                |
| `deprovision_node`        | Remove node from network, revoke mesh access         | Quartermaster `DeleteInfrastructureNode` |

`list_nodes` and `nodes://{id}` already exist as MCP resources.

### Billing

- **Stream usage billing is unchanged.** The agent's prepaid balance covers viewer hours, storage, and processing for streams running on their nodes, same as streams on operator nodes.
- **Node operation is free.** The agent provides the compute; FrameWorks does not charge for the node itself.
- **Bandwidth metering** follows existing patterns: Helmsman reports bandwidth via `NodeLifecycleUpdate`, Periscope aggregates usage, Purser deducts from prepaid balance.

### Trust and Safety

| Concern               | Mitigation                                                                             |
| --------------------- | -------------------------------------------------------------------------------------- |
| Token reuse           | Fingerprint binding (MAC + machine-id hash) prevents token reuse on different hardware |
| Node impersonation    | Enrollment tokens are single-use and short-lived by default                            |
| Stale/unhealthy nodes | Foghorn marks stale nodes after heartbeat timeout; excluded from routing               |
| Misbehaving nodes     | Operator can force-set operational mode to `maintenance` via existing admin API        |
| Data integrity        | Agent nodes only serve their own tenant's streams; no cross-tenant data exposure       |
| Resource abuse        | Prepaid balance enforcement: streams fail when balance depletes                        |

---

## Agent ↔ Operator Node Comparison

| Aspect               | Operator Nodes                     | Agent Nodes                                         |
| -------------------- | ---------------------------------- | --------------------------------------------------- |
| Provisioning         | CLI / infra-as-code                | MCP `create_enrollment_token` + Docker/binary       |
| Trust level          | High (operator controls hardware)  | Lower (agent controls hardware)                     |
| Routing scope        | Global pool or dedicated           | Tenant-scoped (agent's streams only)                |
| Billing              | Operator pays infrastructure costs | Agent provides compute; prepaid covers stream usage |
| Lifecycle management | Operator drains/maintains          | Agent manages via MCP tools                         |
| Mesh membership      | Full mesh access                   | Scoped mesh (only peers needed for tenant traffic)  |

---

## Future Phases

### Phase 2: Edge-Local MCP

Agent communicates directly with its own nodes for real-time operations:

- MCP endpoint running on Helmsman (subset of tools: health, config, metrics).
- Authenticated via the same wallet signature or a node-scoped session token.
- Reduces gateway round-trips for latency-sensitive diagnostics.

### Phase 3: Node Marketplace

Agent nodes can opt in to serving other tenants' traffic:

- Foghorn routes external viewers to the node when capacity is available.
- Agent earns credits (balance top-up) proportional to bandwidth served.
- Requires trust scoring, SLA enforcement, and dispute resolution.

### Phase 4: Agent-Managed Clusters

Agents can manage groups of nodes as a cluster:

- Cluster-level configuration and scaling policies.
- Cluster health dashboard via MCP resources.
- Auto-scaling triggers based on viewer demand.

---

## Implementation Effort

| Component                                         | Effort | Notes                                                 |
| ------------------------------------------------- | ------ | ----------------------------------------------------- |
| `create_enrollment_token` MCP tool                | Small  | Wraps existing Quartermaster RPC                      |
| `set_node_mode` MCP tool                          | Small  | Wraps existing Foghorn API                            |
| `get_node_health` MCP tool                        | Small  | Reads Foghorn node state                              |
| `deprovision_node` MCP tool                       | Small  | Wraps existing Quartermaster RPC                      |
| Docker Compose template for agent nodes           | Small  | Package existing Helmsman + Privateer + MistServer    |
| Tenant-scoped enrollment tokens                   | Small  | Add `tenant_id` to token creation (may already exist) |
| Documentation updates (skill.md, agent-access.md) | Small  | Add node provisioning section                         |

Most of the infrastructure already exists. The primary work is exposing existing RPCs as tenant-scoped MCP tools and packaging the node stack for self-service deployment.

---

## Related

- `docs/architecture/agent-access.md` — Agent authentication, billing, MCP
- `docs/architecture/viewer-routing.md` — Foghorn routing algorithm
- `docs/rfcs/node-drain.md` — Node drain workflow
- `docs/skills/skill.md` — Agent skill entry point

# RFC: Per-Tenant WireGuard Mesh Segments

## Status
Draft

## TL;DR
- Current mesh is per-cluster; tenants within a cluster share the same WireGuard network.
- Add optional per-tenant mesh segments for dedicated B2B isolation.
- Keep shared infrastructure on the shared mesh.

## Current State (as of 2026-01-13)
- Infrastructure nodes are tied to clusters, not tenants.
- Privateer applies WireGuard configs per node from Quartermaster, with a single mesh per cluster.

Evidence:
- `pkg/database/sql/schema/quartermaster.sql` (infrastructure_nodes has no `tenant_id`)
- `api_mesh/internal/agent/agent.go`

## Problem / Motivation
B2B customers may require network-level isolation even when sharing a cluster. The current shared mesh does not provide tenant-specific segmentation.

## Goals
- Optional per-tenant mesh segments within a cluster.
- Shared infra remains reachable from tenant meshes.
- Preserve current behavior for shared-tier tenants.

## Non-Goals
- Full multi-interface routing for all nodes in v1.
- Cross-tenant isolation at the application layer (handled elsewhere).

## Proposal
- Add `tenant_id` to `infrastructure_nodes` for dedicated nodes.
- Allocate per-tenant CIDR ranges within a cluster.
- Mesh sync returns shared nodes to all, tenant nodes only to that tenant.

## Impact / Dependencies
- Quartermaster schema + mesh sync APIs.
- Privateer mesh config generation.
- DNS resolution for tenant-specific zones (optional).

## Alternatives Considered
- Dedicated clusters per tenant.
- Overlay networks (VXLAN) instead of multiple WireGuard interfaces.

## Risks & Mitigations
- Risk: routing complexity increases. Mitigation: phased rollout + clear defaults.
- Risk: IP range collisions. Mitigation: allocator table with uniqueness constraints.

## Migration / Rollout
1. Add schema + allocator tables.
2. Update mesh sync query for tenant visibility.
3. Add optional multi-interface support.

## Open Questions
- When is per-tenant mesh required vs dedicated cluster?
- Do we need tenant-specific DNS zones in v1?

## References, Sources & Evidence
- `pkg/database/sql/schema/quartermaster.sql`
- `api_mesh/internal/agent/agent.go`
- `pkg/proto/quartermaster.proto`

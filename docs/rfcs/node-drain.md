# RFC: Node Drain & Maintenance Mode (Routing + DNS)

## Status

Partially implemented. Node-level `normal` / `draining` / `maintenance` operational modes exist through Foghorn HTTP/gRPC, Commodore/Gateway node-management surfaces, Helmsman control messages, and the CLI/API management flows. Per-role drain flags and DNS pool removal remain future work.

## TL;DR

- Introduce a drain/maintenance state per node and role (ingest/edge/storage/processing).
- Foghorn stops routing new traffic to drained nodes while letting existing sessions complete.
- Optional DNS integration removes drained nodes from pooled records.

## Current State

- Node-level operational modes exist: `normal`, `draining`, and `maintenance`.
- Foghorn exposes `PUT /nodes/:node_id/mode` and `GET /nodes/:node_id/drain-status`.
- Foghorn gRPC exposes `SetNodeOperationalMode`.
- Commodore exposes a node-management RPC for setting operational mode, and Gateway/MCP can call the same node-management surface.
- Helmsman/Foghorn IPC carries `ModeChangeRequest`, `Register.requested_mode`, node lifecycle `operational_mode`, and authoritative `ConfigSeed.operational_mode`.
- Foghorn persists maintenance/mode state in `foghorn.node_maintenance`.
- Not implemented yet: per-role drain state, TTL/expiry semantics, and Navigator DNS pool removal for drained nodes.

Evidence:

- `pkg/database/sql/schema/foghorn.sql`
- `pkg/proto/foghorn.proto`
- `pkg/proto/commodore.proto`
- `pkg/proto/ipc.proto`
- `api_balancing/cmd/foghorn/main.go`
- `api_balancing/internal/handlers`

## Problem / Motivation

Operators need to perform maintenance without disrupting active sessions. Without a drain flag, maintenance requires manual coordination and risks sending new traffic to nodes that should be taken out of service.

## Goals

- Stop new traffic to a node while allowing existing sessions to complete.
- Support per-role drain flags (ingest, edge, storage, processing).
- Expose drain state via control-plane APIs.
- Optional DNS integration for pooled records.

## Non-Goals

- Forcing active RTMP/SRT ingest sessions to migrate.
- Auto-migrating active viewer sessions.

## Proposal

- Keep the implemented node-level operational mode as the default drain primitive.
- Add optional per-role drain flags if operators need to drain ingest, edge, storage, or processing independently.
- Decide whether Quartermaster should persist desired operational mode, or whether Foghorn-local state plus Helmsman config seed remains authoritative enough.
- Optional: api_dns removes drained nodes from pooled records while leaving per-node records.

## Impact / Dependencies

- Quartermaster schema + gRPC APIs.
- Foghorn routing logic.
- Helmsman node lifecycle reporting.
- Optional: Navigator (api_dns) pooled record management.

## Alternatives Considered

- Manual maintenance windows without system support.
- Foghorn-local drain only (no control-plane persistence).

## Risks & Mitigations

- Risk: stale drain flags cause capacity loss. Mitigation: explicit TTL or last-updated monitoring.
- Risk: DNS TTL delays drain effect. Mitigation: keep DNS optional and rely on Foghorn routing.

## Migration / Rollout

1. Keep current node-level mode API (`normal`, `draining`, `maintenance`) as Phase 1.
2. Add per-role drain flags only if real maintenance workflows require them.
3. Add TTL/expiry or stale-mode monitoring if stale drain states become a capacity risk.
4. Optional DNS integration.

## Open Questions

- Who is the long-term authoritative source of desired drain state: Quartermaster, Foghorn-local overrides, or the current Foghorn → Helmsman config seed?
- Should drain flags expire automatically?
- Are per-role drain flags needed, or is node-level mode sufficient for the current edge architecture?

## References, Sources & Evidence

- `pkg/database/sql/schema`
- `pkg/proto`
- `api_balancing/`
- `api_sidecar/`
- `api_dns/`

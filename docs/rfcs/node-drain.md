# RFC: Node Drain & Maintenance Mode (Routing + DNS)

## Status
Draft

## TL;DR
- Introduce a drain/maintenance state per node and role (ingest/edge/storage/processing).
- Foghorn stops routing new traffic to drained roles while letting existing sessions complete.
- Optional DNS integration removes drained nodes from pooled records.

## Current State (as of 2026-01-13)
- No explicit drain state or drain APIs found in repo.
- Routing decisions appear to rely on current node state/health without a maintenance flag.

Evidence:
- No drain fields in `pkg/database/sql/schema/*` (search for "drain").
- No drain RPCs in `pkg/proto/*`.

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
- Add drain flags per node and role in Quartermaster.
- Foghorn respects drain flags when selecting nodes.
- Helmsman reports drain status in node lifecycle updates.
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
1. Add drain flags + APIs in Quartermaster.
2. Update Foghorn routing to respect drain.
3. Add Helmsman reporting.
4. Optional DNS integration.

## Open Questions
- Who is the authoritative source of drain state (Quartermaster vs Foghorn-local overrides)?
- Should drain flags expire automatically?

## References, Sources & Evidence
- `pkg/database/sql/schema/quartermaster.sql`
- `pkg/proto/quartermaster.proto`
- `api_balancing/`
- `api_sidecar/`
- `api_dns/`

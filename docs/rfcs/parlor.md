# RFC: Parlor (Interactive Rooms)

## Status
Draft

## TL;DR
- Introduce a new service for persistent, tenant-owned rooms with realtime presence.
- Keep MVP small: rooms, participants, stage roles, and realtime updates.
- Defer aspirational features (chat, economy, games).

## Current State (as of 2026-01-13)
- `api_rooms` is a stub only; no implementation exists.
- No GraphQL or gRPC surface for rooms.

Evidence:
- `api_rooms/README.md`

## Problem / Motivation
We need a lightweight, tenant-owned room primitive to support interactive experiences without coupling to streaming internals.

## Goals
- Durable rooms scoped to tenant.
- Realtime presence and role changes.
- Clean API surface (GraphQL + internal gRPC).

## Non-Goals
- Full chat system.
- Moderation workflows.
- Economy or rewards in v1.

## Proposal
MVP scope:
- Room CRUD.
- Participant join/leave + role updates.
- Presence events via Signalman.

## Impact / Dependencies
- New service `api_rooms`.
- Bridge GraphQL schema.
- Signalman for realtime presence.

## Alternatives Considered
- Embed room state inside existing services (Bridge/Signalman).
- Use third-party room providers.

## Risks & Mitigations
- Risk: scope creep. Mitigation: strict MVP and non-goals.
- Risk: realtime scalability. Mitigation: Signalman-backed presence.

## Migration / Rollout
1. Implement room core (CRUD + presence).
2. Add client subscriptions.
3. Expand to additional features if needed.

## Open Questions
- Should rooms exist without an associated stream by default?
- How should room permissions be modeled (role vs ACL)?

## References, Sources & Evidence
- `api_rooms/README.md`
- `api_realtime/`
- `pkg/graphql/schema.graphql`

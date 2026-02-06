# RFC: Parlor (Interactive Rooms)

## Status

Draft

## TL;DR

- Introduce a new service for persistent, tenant-owned rooms with realtime presence.
- Keep MVP small: rooms, participants, stage roles, and realtime updates.
- **Phase 2**: Viewer engagement economy (channel points, hype trains, leaderboards, flair).

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

## Non-Goals (MVP / Phase 1)

- Full chat system.
- Moderation workflows.
- Economy or rewards in Phase 1 (see Phase 2 below).

## Proposal

### Phase 1: MVP (Room Primitives)

- Room CRUD.
- Participant join/leave + role updates.
- Presence events via Signalman.

### Phase 2: Viewer Engagement Economy

After MVP stabilizes, add viewer engagement features:

- **Channel points** - Free currency earned by watching, redeemable for streamer-defined perks
- **Hype trains** - Collective momentum from donations/subs; levels with community rewards
- **Leaderboards** - Top donors, watch time, points spent (stream/weekly/monthly/all-time)
- **Viewer flair** - Badges (subscriber, VIP, mod, top donor, founder, custom)
- **Event sync** - Events carry `display_at` = `occurred_at` + stream delay for overlay timing

## Impact / Dependencies

**Phase 1:**

- New service `api_rooms`.
- Bridge GraphQL schema.
- Signalman for realtime presence.

**Phase 2:**

- Parlor schema extensions (points, rewards, hype trains, leaderboards, flair).
- Foghorn integration (stream delay for event sync).
- Purser integration (if donations/subs tied to billing).
- Player/overlay SDK (render events at correct time).

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

**Phase 1:**

- Should rooms exist without an associated stream by default?
- How should room permissions be modeled (role vs ACL)?

**Phase 2:**

- How are channel points earned cross-platform (web vs mobile vs embedded)?
- Should hype train levels/goals be configurable per room?
- How to handle point balance disputes or refunds?
- Should leaderboards be public or opt-in per viewer?
- How to integrate with paid subscriptions from Purser?

## References, Sources & Evidence

- `api_rooms/README.md`
- `api_realtime/`
- `pkg/graphql/schema.graphql`
- [Reference] Industry patterns for viewer loyalty programs and gamification

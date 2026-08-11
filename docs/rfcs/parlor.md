# RFC: Parlor (Interactive Rooms)

## Status

Draft

## TL;DR

- Introduce a new service for persistent, tenant-owned rooms with realtime presence.
- Keep MVP small: rooms, participants, stage roles, and realtime updates.
- **Phase 2**: Viewer engagement economy (channel points, hype trains, leaderboards, flair).

## Current State

- `api_rooms` is a stub only; no implementation exists.
- No GraphQL or gRPC surface for rooms.
- Signalman's hub fans out five channel types (STREAMS/ANALYTICS/SYSTEM/MESSAGING/AI); it has no presence/room channel type. The realtime substrate Phase 1 needs does not exist yet.

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
- Presence events via Signalman. This requires a new presence/room channel type in Signalman — its hub currently fans out five channel types (STREAMS/ANALYTICS/SYSTEM/MESSAGING/AI), none of which is a presence/room channel. Building that channel type is part of Phase 1 scope, not a reuse of existing capability.

### Phase 2: Viewer Engagement Economy

After MVP stabilizes, add viewer engagement features:

- **Channel points** - Free currency earned by watching, redeemable for streamer-defined perks
- **Hype trains** - Collective momentum from donations/subs; levels with community rewards
- **Leaderboards** - Top donors, watch time, points spent (stream/weekly/monthly/all-time)
- **Viewer flair** - Badges (subscriber, VIP, mod, top donor, founder, custom)
- **Event sync** - Events bind to a stream-timeline position rather than wall clock. Each viewer receives an event when that timeline position enters their playback view, using the per-viewer latency the analytics pipeline already measures — though today that signal is post-hoc beacon telemetry aggregated for analytics, not a live delivery-time input, so a realtime feed of it is part of the build. A single global stream-delay offset is not enough: viewers sit at different distances behind the live edge, so a shared offset misaligns overlays for everyone but the average viewer. Timeline-bound delivery keeps hype train levels, point redemptions, and flair reveals synced to what each viewer actually sees. Competitive or transactional interactions (auctions, timed drops) need the same delivery mechanism plus a single authoritative truth on the server side — see `docs/rfcs/live-commerce.md` (Latency fairness).

## Impact / Dependencies

**Phase 1:**

- New service `api_rooms`.
- Bridge GraphQL schema.
- Signalman for realtime presence — build dependency: a presence/room channel type must be added to Signalman first (see Current State).

**Phase 2:**

- Parlor schema extensions (points, rewards, hype trains, leaderboards, flair).
- Foghorn integration (per-stream delay signal for event sync).
- Analytics pipeline (per-viewer playback latency, already measured) for timeline-bound delivery.
- Purser integration (if donations/subs tied to billing).
- Player/overlay SDK (render events when their timeline position is in view).

### Owning services / modules

- **Parlor (`api_rooms`)** — room primitives, participants, roles; Phase 2 engagement economy state (points, hype trains, leaderboards, flair) and the timeline binding on emitted events.
- **Signalman (`api_realtime`)** — realtime fan-out; gains a presence/room channel type (Phase 1 build dependency) and per-viewer timeline-bound event delivery (Phase 2).
- **Bridge (`api_gateway`)** — GraphQL surface and subscriptions for rooms and engagement events.
- **Foghorn (`api_balancing`)** — per-stream delay signal; per-viewer playback latency comes from the analytics pipeline.

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
- `pkg/graphql`
- [Reference] `docs/rfcs/live-commerce.md` (Latency fairness — competitive/timeline-fair interactions)
- [Reference] Industry patterns for viewer loyalty programs and gamification

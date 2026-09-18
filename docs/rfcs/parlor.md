# RFC: Parlor (Interactive Rooms)

## Status

Draft

## TL;DR

- Parlor is the room layer: tenant-owned rooms with participants, stage roles, and realtime presence. Chat and conferencing are both products built on it; neither owns its own session model.
- Moderators and producers are tenant team members, so Parlor builds on Teams & RBAC (`team-rbac`). Room roles (host, guest, viewer, moderator) are room-scoped and separate from tenant roles.
- Media never passes through Parlor. For conferencing, Parlor authorizes a guest and issues a room-scoped WHIP publish grant; Mist/Foghorn carry the media and multistream compositing mixes it into the program stream.
- Stream-synced events use in-band timeline cues carried in the stream itself, not a per-viewer latency feed. SCTE-35 is the same kind of cue, so this phase starts by merging SCTE-35 support into our MistServer fork.
- The viewer engagement economy (channel points, hype trains, leaderboards, flair) comes last, on top of timeline cues and stream-level balances.

## Current State

- `api_rooms` is a stub only; no implementation exists.
- No GraphQL or gRPC surface for rooms.
- Signalman's hub fans out five channel types (STREAMS/ANALYTICS/SYSTEM/MESSAGING/AI); it has no presence/room channel type. The realtime substrate Phase 1 needs does not exist yet.
- Tenants are single-user; there is no membership or role model to hang moderator/producer permissions on (`team-rbac` is roadmap).
- WHIP ingest and WHEP playback ship as single-source paths; there is no multi-participant session model or participant-scoped publish authorization.
- SCTE-35 support exists only on unmerged MistServer branches. The most recent is `upstream/EFG/SCTE35` (last commit 2025-06-30, 105 commits ahead of our fork's `master`, including an `inject_scte35` API call). It is feature-complete but untested in our stack.

Evidence:

- `api_rooms/README.md`
- `api_realtime/`
- `docs/platform-features.yaml` (`rooms-interactivity`, `conferencing`, `team-rbac`)
- `../mistserver` remote branch `upstream/EFG/SCTE35`

## Problem / Motivation

Chat, remote contribution, and stream-synced interactivity all need the same thing: a durable room that knows who is in it, what each participant may do, and who is present right now. Building that separately for each product would give three membership models and three authorization paths. Parlor is that shared primitive, kept out of the streaming internals.

## Goals

- Durable rooms scoped to tenant, optionally bound to a stream.
- Realtime presence and role changes.
- One authorization point for participant actions, including publishing media into a room.
- Moderation from the first chat release.
- Interactive events that fire at the right stream position for every viewer, regardless of how far behind the live edge they watch.
- Clean API surface (GraphQL + internal gRPC + MCP).

## Non-Goals

- Carrying or mixing media inside Parlor (Mist/Foghorn and compositing own the media plane).
- Replacing tenant RBAC with room roles.
- Economy or rewards before timeline cues and stream-level balances exist.

## Proposal

### Prerequisite: Teams & RBAC

Tenant membership, invitations, and roles (`team-rbac`). Moderators, producers, and hosts are team members with a tenant role; Parlor checks tenant roles for management actions (create room, assign moderators) and room roles for in-room actions.

### Phase 1: Room core

- Room CRUD, optionally bound to a stream.
- Participants: join/leave, stage roles (host, guest, viewer, moderator), role changes.
- Presence via a new presence/room channel type in Signalman. Building that channel type is part of this phase, not a reuse of existing capability.
- Participant state includes a moderation status (muted, timed out, banned) so every product built on rooms enforces it the same way.

### Phase 2a: Remote contribution / conferencing

Built on room core plus multistream compositing.

- A host invites a guest into a room; Parlor issues a short-lived, room-scoped WHIP publish grant bound to the participant and role.
- Guest media is ingested through the normal Foghorn/Mist path; compositing mixes it into the room's program stream.
- Audio first (live commentator / caller workflow), video after.
- Revoking a role or banning a participant revokes the publish grant.

### Phase 2b: Chat and moderation

- Room-scoped chat on the Signalman room channel.
- Moderation is part of the first release: bans and timeouts, rate limits, slow mode, message deletion, and a per-participant history view (all messages and actions for a user, with moderation actions recorded).

2a and 2b are independent once Phase 1 lands.

### Phase 3: Stream-timeline events

Events bind to a stream timeline position and are carried in-band as timed metadata, so the player fires each event when its own playback reaches that position. This handles every viewer's distance from the live edge without a per-viewer latency feed; a single global delay offset would be wrong for everyone except the average viewer.

- **First step: merge SCTE-35 into our MistServer fork (`../mistserver`).** SCTE-35 splice markers are timeline cues, so Parlor cues and ad markers share one carriage path from the start. If upstream has merged SCTE-35 by then, rebase onto upstream instead. Otherwise merge `upstream/EFG/SCTE35` (or its successor) into the fork, then add tests for passthrough (ingest → HLS/CMAF output), `inject_scte35`, and timestamp rollover.
- Parlor emits cues through Mist's cue injection; the player SDK exposes them as timeline events.
- Competitive or transactional interactions (auctions, timed drops) need the same delivery plus one authoritative server-side truth; see `docs/rfcs/live-commerce.md` (Latency fairness).

### Phase 4: Viewer engagement economy

Built on timeline cues (Phase 3) and stream-level balances (`docs/rfcs/stream-balances.md`).

- **Channel points** - free currency earned by watching, redeemable for streamer-defined perks
- **Hype trains** - collective momentum from donations/subs; levels with community rewards
- **Leaderboards** - top donors, watch time, points spent (stream/weekly/monthly/all-time)
- **Viewer flair** - badges (subscriber, VIP, mod, top donor, founder, custom)
- **Viewer games** - viewers join a streamer-run game (e.g. a marble race) with points carried across games

Hype train levels, redemptions, and flair reveals fire as Phase 3 timeline events.

## Impact / Dependencies

- **Teams & RBAC** - tenant membership and roles (prerequisite).
- **Parlor (`api_rooms`)** - new service: rooms, participants, roles, moderation state, publish grants, cue emission, economy state.
- **Signalman (`api_realtime`)** - presence/room channel type (Phase 1), chat fan-out (Phase 2b).
- **Bridge (`api_gateway`)** - GraphQL surface and subscriptions.
- **Foghorn (`api_balancing`) / Mist** - validate room-scoped WHIP publish grants (Phase 2a); carry timeline cues (Phase 3).
- **Multistream compositing** - mixes guest media into the program stream (Phase 2a).
- **MistServer fork (`../mistserver`)** - SCTE-35 merge and cue injection (Phase 3).
- **Player SDK** - timeline event API (Phase 3), overlay rendering (Phase 4).
- **Purser** - stream-level balances, donations/subs (Phase 4).

### Owning services / modules

- **Parlor (`api_rooms`)** - room primitives, participants, roles, moderation state, publish grants, timeline cue emission, engagement economy state.
- **Signalman (`api_realtime`)** - realtime fan-out for presence and chat.
- **Bridge (`api_gateway`)** - GraphQL surface and subscriptions for rooms, chat, and engagement events.
- **Foghorn (`api_balancing`)** - admission of participant WHIP publishes against Parlor grants.

## Alternatives Considered

- Separate session models for chat and conferencing: duplicates membership and authorization; rejected.
- Embed room state inside existing services (Bridge/Signalman).
- Use third-party room providers.
- Per-viewer latency feed for event sync: requires turning post-hoc QoE beacons into a realtime delivery input and still drifts when a viewer seeks or rebuffers; in-band cues move with playback by construction.

## Risks & Mitigations

- Risk: scope creep. Mitigation: phases gated on the previous phase; economy last.
- Risk: realtime scalability. Mitigation: Signalman-backed presence.
- Risk: guest-to-guest talkback latency. Mitigation: settle the media topology in Phase 2a planning; this choice sets most of 2a's cost. Mist's WebRTC is server-side, not a room: server-relayed talkback (each guest publishes once over WHIP and pulls the others over WHEP) reuses the ingest path and avoids NAT issues but adds a server hop, while a direct P2P mesh with Parlor as signaling lowers talkback latency but needs NAT traversal, with Mist as relay fallback and hole-punching coordinator (`docs/rfcs/nat-traversal.md`).
- Risk: SCTE-35 branch diverges further from upstream. Mitigation: prefer upstream if merged by Phase 3; otherwise merge with its own test coverage before Parlor depends on it.

## Migration / Rollout

1. Teams & RBAC.
2. Room core (CRUD, participants, roles, presence).
3. Conferencing (audio first) and chat with moderation, in either order.
4. SCTE-35 merge in the Mist fork, then timeline cues end to end.
5. Engagement economy.

## Open Questions

- Should rooms exist without an associated stream by default?
- How should room permissions be modeled (role vs ACL)?
- Guest talkback topology: server-relayed through Mist, direct P2P with Mist as relay/hole-punching fallback, or server-relayed first with P2P as a later latency optimization?
- Which Mist metadata path carries non-SCTE Parlor cues (SCTE-35 private commands vs a separate metadata track), and how does each surface in HLS/CMAF/WebRTC outputs?
- How are channel points earned cross-platform (web vs mobile vs embedded)?
- Should hype train levels/goals be configurable per room?
- How to handle point balance disputes or refunds?
- Should leaderboards be public or opt-in per viewer?

## References, Sources & Evidence

- `api_rooms/README.md`
- `api_realtime/`
- `pkg/graphql`
- `docs/platform-features.yaml` (`rooms-interactivity`, `conferencing`, `team-rbac`)
- `../mistserver` branch `upstream/EFG/SCTE35`
- [Reference] `docs/rfcs/live-commerce.md` (Latency fairness)
- [Reference] `docs/rfcs/stream-balances.md`
- [Reference] Industry patterns for viewer loyalty programs and gamification

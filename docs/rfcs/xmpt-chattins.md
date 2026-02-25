# RFC: Chat Tracks Over MistServer (XMPT Integration)

## Status

Draft

## TL;DR

- Transport chat messages as a JSON data track via MistServer.
- Edge accepts chat ingest and forwards upstream; origin is authoritative.
- Requires MistServer DTSC changes + player integration.

## Current State

- No chat data-track ingest path exists in this repo.
- Player surfaces metadata tracks but does not render chat.

Evidence:

- `npm_player/packages/core/src/core`
- MistServer changes would be external to this repo.

## Problem / Motivation

We want low-latency, stream-synced chat that can be replayed with video and consumed by clients without a separate chat stack.

## Goals

- Treat chat as a first-class data track.
- Keep origin authoritative for validation.
- Allow edge ingest with upstream forwarding.

## Non-Goals

- Full chat UI.
- Trusting edges for final authorization.
- Replacing third-party chat services.

## Proposal

- Define a JSON data track for chat messages (timestamp, sender, message, metadata).
- Edge nodes accept chat ingest and forward upstream over DTSC.
- Origin validates and writes to the data track.
- Player exposes data track events for chat rendering.

## Impact / Dependencies

- MistServer fork/patches (DTSC ingest + data track).
- Edge/origin forwarding logic.
- Player data track consumption + optional UI.

## Alternatives Considered

- External chat service with separate sync.
- WebSocket-only chat without stream-embedded track.

## Risks & Mitigations

- Risk: MistServer changes are non-trivial. Mitigation: prototype with a small bridge service.
- Risk: abuse/spam. Mitigation: origin-side auth + rate limiting.

## Migration / Rollout

1. Prototype data track ingestion in MistServer fork.
2. Add edge forwarder + origin validation.
3. Expose player callbacks for data track events.

## Open Questions

- Should chat be persisted outside of Mist data track?
- How do we rotate auth keys per stream?

## References, Sources & Evidence

- `npm_player/packages/core/src/core`
- MistServer DTSC/JSON track docs (external)

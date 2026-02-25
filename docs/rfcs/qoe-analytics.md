# RFC: QoE Analytics Roadmap

## Status

Draft

## TL;DR

- Current analytics are mostly server-side QoS; true client QoE is missing.
- Add client telemetry (npm_player) and/or Mist playback events to fill gaps.
- Introduce new ClickHouse tables for playback events and session summaries.

## Current State

- Periscope ingests server-side connection and stream health metrics.
- No client-side playback telemetry pipeline is wired.

Evidence:

- `pkg/database/sql/clickhouse`
- `api_analytics_ingest/`
- `npm_player/packages/core/src/core`

## Problem / Motivation

We cannot quantify viewer experience (TTFF, rebuffering, startup failures) with server-only metrics. This blocks product and operational insights.

## Goals

- Capture core QoE metrics (TTFF, rebuffering, failures, quality changes).
- Support both npm_player and non-npm players.
- Keep data volume manageable.

## Non-Goals

- Full Mux parity in v1.
- Advanced ML scoring.

## Proposal

- Add a client telemetry ingest endpoint for npm_player.
- Evaluate MistServer playback events as a proxy for non-npm players.
- Create two tables: `viewer_playback_events` and `viewer_session_summary`.

## Impact / Dependencies

- npm_player SDK changes + telemetry endpoint.
- Periscope ingest and ClickHouse schema.
- Gateway/GraphQL for querying QoE.

## Alternatives Considered

- Only server-side proxies (insufficient fidelity).
- Third-party QoE vendor.

## Risks & Mitigations

- Risk: high data volume. Mitigation: sampling + TTL.
- Risk: privacy concerns. Mitigation: minimize PII and document retention.

## Migration / Rollout

1. Add schema + ingest endpoint.
2. Enable npm_player telemetry for a small cohort.
3. Expand metrics and dashboards.

## Open Questions

- Can MistServer emit playback events reliably for all protocols?
- What retention window is acceptable for raw events?

## References, Sources & Evidence

- `pkg/database/sql/clickhouse`
- `api_analytics_ingest/`
- `npm_player/packages/core/src/core`

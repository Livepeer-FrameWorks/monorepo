# RFC: QoE Analytics Roadmap

## Status

Partially implemented

## TL;DR

- Server-side QoS and client/network QoE tables exist.
- Mist client lifecycle events feed `client_qoe_samples` and `client_qoe_5m`.
- `npm_player` has telemetry reporting primitives, but a general public player telemetry ingest path is not fully productized.

## Current State

- Periscope ingests server-side connection and stream health metrics.
- ClickHouse has `client_qoe_samples`, `client_qoe_5m`, `rebuffering_events`, and viewer session tables/materialized views.
- Periscope Ingest writes Mist client lifecycle updates into `client_qoe_samples`.
- Periscope Query and Gateway expose client QoE summaries/connections.
- `npm_player` exports `TelemetryReporter` and MewsWsPlayer has an analytics endpoint option, but the broader SDK-to-platform telemetry product path still needs hardening and documentation.

Evidence:

- `pkg/database/sql/clickhouse`
- `api_analytics_ingest/`
- `api_analytics_query/`
- `api_gateway/graph`
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

- Harden and document the existing Mist client lifecycle QoE path.
- Productize a client telemetry ingest endpoint/configuration for npm_player.
- Decide whether the existing `client_qoe_*`, `rebuffering_events`, and viewer session tables cover the need, or whether additional playback-event/session-summary tables are still required.

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

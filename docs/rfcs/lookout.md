# RFC: Lookout (Incidents and Alerts)

## Status

Draft

## TL;DR

- Create a central incident/alert service that aggregates signals and exposes a clean incident feed.
- Current monitoring remains Prometheus/Grafana; Lookout is additive.
- Start with a minimal ingestion + incident feed; defer complex workflows.

## Current State

- Monitoring and alerting are handled via Prometheus/Grafana tooling in `infrastructure/`.
- `api_incidents` is a stub (no implementation).

Evidence:

- `infrastructure/prometheus/`
- `infrastructure/grafana/`
- `api_incidents/`

## Problem / Motivation

Alerts are fragmented across tools and are not surfaced as a unified incident feed for dashboards, APIs, or notifications.

## Goals

- Provide a single incident feed with deduplication.
- Integrate with existing Prometheus alerts.
- Surface incidents via GraphQL and realtime events.

## Non-Goals

- Replacing Prometheus/Grafana.
- Full escalation workflows or paging in v1.

## Proposal

- Build a minimal Lookout service that ingests alert webhooks and produces incidents.
- Emit incidents to Bridge (GraphQL) and Signalman (realtime).

## Impact / Dependencies

- New service (`api_incidents`).
- Alert webhook ingestion.
- GraphQL + realtime integrations.

### Downstream Consumer: Skipper

Lookout incident events are a trigger source for Skipper (AI Video Consultant,
see `./mcp-consultant/mcp-consultant.md`). When Lookout fires an incident, Skipper
can automatically investigate the root cause using its diagnostic tool chain and
deliver a pre-investigated report alongside the alert.

This is a **soft dependency** — Skipper's heartbeat agent works independently of
Lookout, but integrating Lookout events enables immediate, event-driven investigation
rather than waiting for the next heartbeat cycle.

Incidents should be tenant-scoped and emitted via Kafka so Skipper can subscribe
to the incident topic.

## Alternatives Considered

- Continue with Prometheus/Grafana only.
- Use a third-party incident platform.

## Risks & Mitigations

- Risk: duplicative alerting. Mitigation: start with read-only incident aggregation.
- Risk: scope creep. Mitigation: strict MVP.

## Migration / Rollout

1. Implement webhook ingestion + incident store.
2. Add GraphQL + realtime feed.
3. Expand integrations only after MVP stabilizes.
4. Skipper integration: subscribe to incident Kafka topic for auto-investigation triggers.

## Open Questions

- What is the minimum incident schema for v1?
- Should incidents be tenant-scoped or global by default?
  - **Leaning**: Tenant-scoped, as Skipper's investigation is per-tenant and requires `tenant_id` for tool calls.

## References, Sources & Evidence

- `infrastructure/prometheus/`
- `infrastructure/grafana/`
- `api_incidents/`
- [Related RFC] ./mcp-consultant/mcp-consultant.md — Skipper consumes Lookout incidents as investigation triggers

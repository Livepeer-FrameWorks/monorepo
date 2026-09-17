# Lookout (Incidents)

Turns Alertmanager notifications into incidents, notifies operator channels,
feeds tenant incidents to Skipper and realtime subscribers, and delivers an
allowlist of direct platform activity through the same process.

## What it does

- Ingests Alertmanager webhooks on `POST /v1/alertmanager` (bearer `LOOKOUT_ALERTMANAGER_TOKEN`) and deduplicates by Alertmanager group key
- Scopes incidents to the tenant that owns the alerting cluster (Quartermaster), otherwise to platform operators
- Auto-resolves when every alert resolves; a manually resolved incident is not reopened by Alertmanager repeats
- Serves `lookout.LookoutService` over gRPC: list, get, acknowledge, assign, resolve, notes, and Skipper investigation attachment
- Delivers platform incident notifications to email, Slack, and Discord, and tenant incidents to Kafka `lookout.incidents`, through a lease-fenced outbox
- Publishes tenant incident updates as `incident_updated` service events for Signalman
- Delivers source-checked signup, product, billing, support, and marketing
  activity events to configured Slack and Discord destinations through a
  separate lease-fenced outbox

## Ports

- HTTP 18022 (`/health`, `/metrics`, `/v1/alertmanager`)
- gRPC 19008

## Development

- Unit tests: `make test-lookout`
- Real PostgreSQL: `make verify-lookout-db`
- Schema: `pkg/database/sql/schema/lookout.sql`, queries in `internal/database/queries` (`make sqlc`)

Architecture: [`docs/architecture/lookout.md`](../docs/architecture/lookout.md). Operator setup: `website_docs/src/content/docs/operators/alerting.mdx`.

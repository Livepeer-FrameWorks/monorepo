# Lookout (Incident Management Service)

Status: Deferred (post‑MVP). Today Prometheus + Grafana cover monitoring and alerting needs.

Current solution:
- Prometheus for metrics collection and alert rules
- Grafana for dashboards and alert notifications
- VictoriaMetrics for high-performance metrics storage

Rationale: The Prometheus/Grafana stack meets current requirements; Lookout can be revisited when needs outgrow the existing setup.

However, multiple teams (telemetry, developer API, Signalman consumers) are already expecting a unified incident/alert feed. This service will eventually mediate between Periscope/raw metrics and downstream consumers so the bridge no longer infers alerts ad hoc.

## Overview

Lookout (a.k.a. `api_incidents`) will become the single source of truth for active incidents and alert streams. It will:

- Ingest raw alerts/telemetry from Periscope, infrastructure services, billing, etc.
- Run deduplication & correlation so a flood of metric samples becomes a single incident record.
- Expose those incidents via REST/GraphQL as well as publish updates to Signalman, so dashboards, the webapp, and third-party clients get a consistent feed.
- Orchestrate notification/escalation workflows and feed other internal tooling (Deckhand, Privateer, etc.).

## Core Features (planned)

- **Alert Aggregation** - Collect from Prometheus/VictoriaMetrics, Periscope health metrics, Purser billing anomalies, etc.
- **Smart Deduplication** - Group related alerts/metric spikes into a single incident entity.
- **Incident Bus** - Publish incident lifecycle events over Signalman (and potentially webhooks) so bridge/webapp/developer API consumers can subscribe without reimplementing alert logic.
- **Escalation Policies** - Automatic escalation based on severity and time.
- **Multi-Channel Notifications** - Slack, Discord, Email, SMS, PagerDuty.
- **Status Page** - Public incident status and history.

## Architecture (future)

```
Periscope Metrics ─┐
Infra Signals  ────┼─> Ingestion ➜ Correlation/Dedup ➜ Incident Store ➜
Billing Events  ───┘                                        ↓
                                           Notification & Signalman Publisher
                                                            ↓
                                [Bridge GraphQL, Webapp, CLI, third-party consumers]
```

## Configuration

Configuration will be provided via an `env.example` file (with inline comments) if/when this service is implemented. Use the file as the source of truth for environment variables.

## Alert Rules

```yaml
# Example alert configuration
rules:
  - name: node_down
    query: up{job="node"} == 0
    duration: 1m
    severity: critical
    
  - name: high_cpu
    query: cpu_usage > 0.9
    duration: 5m
    severity: warning
    
  - name: stream_failure
    query: stream_health < 0.5
    duration: 30s
    severity: high
```

## Integration Points

- **Periscope / ClickHouse** - Long-term source for telemetry-derived incidents (replacing ad-hoc heuristics in the bridge).
- **Prometheus/VictoriaMetrics** - Pull infrastructure alerts via API.
- **Deckhand** - Create support tickets for customer-facing incidents.
- **Privateer** - Monitor mesh connectivity.
- **Signalman** - Push incident updates to dashboards and developer clients (authoritative alert feed).
- **GraphQL Gateway** - Eventually `streamHealthAlerts` should proxy this service instead of computing alerts inline.

## Database Schema

```sql
CREATE TABLE incidents (
    id UUID PRIMARY KEY,
    title VARCHAR(255) NOT NULL,
    description TEXT,
    severity VARCHAR(50) NOT NULL,
    status VARCHAR(50) NOT NULL,
    triggered_at TIMESTAMP NOT NULL,
    acknowledged_at TIMESTAMP,
    resolved_at TIMESTAMP,
    closed_at TIMESTAMP
);

CREATE TABLE alerts (
    id UUID PRIMARY KEY,
    incident_id UUID REFERENCES incidents(id),
    source VARCHAR(100) NOT NULL,
    name VARCHAR(255) NOT NULL,
    query TEXT,
    value FLOAT,
    threshold FLOAT,
    received_at TIMESTAMP NOT NULL
);

CREATE TABLE escalations (
    id UUID PRIMARY KEY,
    incident_id UUID REFERENCES incidents(id),
    level INTEGER NOT NULL,
    notified_at TIMESTAMP NOT NULL,
    acknowledged BOOLEAN DEFAULT false
);
```

## API Endpoints

- `POST /alerts` - Receive alert from monitoring system
- `GET /incidents` - List all incidents
- `POST /incidents/{id}/acknowledge` - Acknowledge incident
- `POST /incidents/{id}/resolve` - Resolve incident
- `GET /oncall` - Get current on-call schedule
- `GET /status` - Public status page data

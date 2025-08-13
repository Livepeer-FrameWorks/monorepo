# Lookout (Incident Management Service)

> **Status**: 🚧 **Planned** - Essential for production operations

## Overview

Lookout provides intelligent incident management by aggregating alerts from monitoring systems, deduplicating related issues, and orchestrating response workflows.

## Core Features

- **Alert Aggregation** - Collect from Prometheus, Grafana, and custom sources
- **Smart Deduplication** - Group related alerts into single incidents
- **Escalation Policies** - Automatic escalation based on severity and time
- **Multi-Channel Notifications** - Slack, Discord, Email, SMS, PagerDuty
- **Status Page** - Public incident status and history

## Architecture

```
Prometheus ─┐
            ├→ Alert Ingestion → Deduplication → Incident Creation
Grafana ────┤                            ↓
            │                    Notification Engine
Custom ─────┘                            ↓
                              [Slack, Email, SMS, Status Page]
```

## Configuration

Environment variables:
- `PORT` - API port (default: 18013)
- `DATABASE_URL` - PostgreSQL connection string
- `PROMETHEUS_URL` - Prometheus/VictoriaMetrics endpoint
- `SLACK_WEBHOOK_URL` - Slack notification webhook
- `SMTP_HOST`, `SMTP_PORT`, `SMTP_USER`, `SMTP_PASS` - Email config

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

- **Prometheus/VictoriaMetrics** - Pull alerts via API
- **Deckhand** - Create support tickets for customer-facing incidents
- **Privateer** - Monitor mesh connectivity
- **Signalman** - Push incident updates to dashboards

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

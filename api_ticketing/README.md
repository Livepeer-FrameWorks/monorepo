# Deckhand (Support Ticketing Service)

> **Status**: 🚧 **Planned** - High priority support service

## Overview

Deckhand provides comprehensive support ticketing with streaming-specific context, handling customer inquiries, technical issues, and internal escalations from Lookout incidents.

## Core Features

- **Stream-Aware Tickets** - Automatically attach stream metadata and diagnostics
- **Multi-Channel Intake** - Email, web forms, chat integration, API
- **SLA Management** - Response time tracking and escalation policies
- **Knowledge Base** - Self-service articles and troubleshooting guides
- **Agent Routing** - Skills-based assignment and workload balancing

## Architecture

```
Email/Web/API ──┐
                ├→ Ticket Intake → Classification → Agent Assignment
Chat/Webhook ───┤                         ↓
                │                 Stream Context Lookup
Incidents ──────┘                         ↓
                                 [Agent Dashboard, Customer Portal]
```

NOTE: We might want to deprecate api_forms and roll it into the Deckhand.

Configuration: this service is planned. When implemented, configuration will be provided via an `env.example` file with inline comments.

## Integration Points

- **Commodore** - Customer authentication and stream ownership
- **Lookout** - Auto-create tickets for customer-facing incidents
- **Signalman** - Real-time agent notifications
- **Quartermaster** - Tenant context and escalation contacts

## Database Schema

```sql
CREATE TABLE tickets (
    id UUID PRIMARY KEY,
    number SERIAL UNIQUE NOT NULL,
    subject VARCHAR(255) NOT NULL,
    description TEXT,
    status VARCHAR(50) NOT NULL,
    priority VARCHAR(50) NOT NULL,
    tenant_id UUID,
    stream_id UUID,
    customer_email VARCHAR(255),
    assigned_agent VARCHAR(255),
    created_at TIMESTAMP NOT NULL,
    updated_at TIMESTAMP NOT NULL,
    resolved_at TIMESTAMP,
    sla_due TIMESTAMP
);

CREATE TABLE ticket_messages (
    id UUID PRIMARY KEY,
    ticket_id UUID REFERENCES tickets(id),
    author_email VARCHAR(255) NOT NULL,
    message TEXT NOT NULL,
    is_internal BOOLEAN DEFAULT false,
    created_at TIMESTAMP NOT NULL
);

CREATE TABLE knowledge_articles (
    id UUID PRIMARY KEY,
    title VARCHAR(255) NOT NULL,
    content TEXT NOT NULL,
    tags TEXT[],
    view_count INTEGER DEFAULT 0,
    created_at TIMESTAMP NOT NULL,
    updated_at TIMESTAMP NOT NULL
);
```

## API Endpoints

- `POST /tickets` - Create new ticket
- `GET /tickets` - List tickets (filtered by agent/tenant)
- `GET /tickets/{id}` - Get ticket details and messages
- `POST /tickets/{id}/messages` - Add message to ticket
- `PUT /tickets/{id}` - Update ticket status/assignment
- `GET /knowledge` - Search knowledge base
- `GET /metrics` - Support team metrics

# Deckhand (Support Ticketing Service)

Status: Planned — tracked on the roadmap. Not implemented yet.

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

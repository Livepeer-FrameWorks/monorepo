# RFC: Flow Automation (Workflows)

## Status

Draft

## TL;DR

- Shopify Flow-style workflow engine: trigger → condition → action
- API-first with visual builder in webapp
- Enables auto-clip, alerts, webhooks without custom integration code
- Integrates with AI detection for intelligent triggers (scene detection, object recognition)

## Current State

- No workflow automation system exists
- Tenants must build custom integrations to react to events
- Disabled "Automations" page exists in webapp (placeholder)
- Events flow through Kafka but no tenant-configurable reactions

## Problem / Motivation

Tenants want automated actions based on events without building custom integrations:

- Auto-create highlight clip when stream ends
- Alert Slack when concurrent viewers exceed threshold
- Send webhook to CRM when new stream created
- Auto-archive streams older than retention period
- **AI-driven**: Auto-clip when goal is scored, person detected, scene changes

Currently this requires custom code polling the API or setting up webhook consumers.

## Goals

- Declarative workflow definitions (YAML/JSON)
- Event-driven execution via Kafka
- API for programmatic workflow management
- Visual builder for non-technical users
- Pre-built templates for common patterns
- **AI trigger integration** for intelligent automation

## Non-Goals

- Complex branching/looping logic in v1
- Human-in-the-loop approvals
- Cross-tenant workflows
- Real-time latency guarantees (eventual consistency is fine)
- Training custom AI models (use pre-trained or third-party)

## Proposal

### Workflow Model

```yaml
name: Auto-clip on stream end
trigger:
  type: stream.ended
  filters:
    duration_seconds: { gte: 300 } # Only streams > 5 min
conditions:
  - field: stream.viewer_peak
    operator: gte
    value: 100
actions:
  - type: create_clip
    params:
      duration_seconds: 60
      title: "Stream Highlight - {{stream.title}}"
  - type: send_webhook
    params:
      url: "{{tenant.webhook_url}}"
      payload:
        event: clip_created
        stream_id: "{{stream.id}}"
```

### AI-Driven Workflow Example

```yaml
name: Auto-clip on goal detection
trigger:
  type: ai.detection
  filters:
    model: sports_highlight
    label: goal
    confidence: { gte: 0.85 }
actions:
  - type: create_clip
    params:
      start_offset_seconds: -10 # 10 seconds before detection
      duration_seconds: 30
      title: "GOAL! - {{stream.title}}"
  - type: send_notification
    params:
      message: "Goal detected and clipped automatically"
```

### Triggers

| Trigger                   | Source                 | Description                 |
| ------------------------- | ---------------------- | --------------------------- |
| `stream.started`          | Kafka `service_events` | Stream goes live            |
| `stream.ended`            | Kafka `service_events` | Stream ends                 |
| `stream.viewer_threshold` | Periscope              | Viewers cross threshold     |
| `billing.usage_threshold` | Purser                 | Usage exceeds percentage    |
| `webhook.received`        | External HTTP          | Incoming webhook            |
| `schedule.cron`           | Internal               | Cron expression             |
| **`ai.detection`**        | AI Pipeline            | Object/scene/event detected |
| **`ai.scene_change`**     | AI Pipeline            | Scene transition detected   |
| **`ai.speech`**           | AI Pipeline            | Speech/keyword detected     |

### AI Trigger Details

The `ai.*` triggers integrate with the processing pipeline:

| AI Trigger        | Use Cases                                                    |
| ----------------- | ------------------------------------------------------------ |
| `ai.detection`    | Goal scored, person appeared, logo visible, explicit content |
| `ai.scene_change` | Cut to replay, switched camera, ended segment                |
| `ai.speech`       | Keyword mentioned, language detected, silence detected       |
| `ai.audio`        | Music detected, crowd cheering, applause                     |

**Detection labels** (configurable per model):

- Sports: `goal`, `foul`, `timeout`, `celebration`
- General: `person`, `face`, `text`, `logo`
- Content: `explicit`, `violence`, `safe`
- Custom: Tenant-provided labels via fine-tuning (future)

### Conditions

- Field comparisons: `stream.duration > 3600`
- Logical operators: `and`, `or`, `not`
- Time windows: `time.hour between 9 and 17`
- Tenant state: `tenant.tier == 'production'`
- **AI confidence**: `detection.confidence >= 0.9`
- **AI label matching**: `detection.label in ['goal', 'celebration']`

### Actions

| Action                   | Service   | Description                    |
| ------------------------ | --------- | ------------------------------ |
| `create_clip`            | Commodore | Create clip from stream        |
| `send_webhook`           | External  | HTTP POST to URL               |
| `send_notification`      | Signalman | Push to webapp                 |
| `send_email`             | Listmonk  | Email via template             |
| `update_stream`          | Commodore | Update stream metadata         |
| `update_vod`             | Commodore | Update VOD metadata            |
| **`add_marker`**         | Commodore | Add timestamp marker to stream |
| **`trigger_processing`** | Foghorn   | Queue processing job           |

### Architecture

```
                    ┌──────────────┐
                    │    Kafka     │
                    │   Events     │
                    └──────┬───────┘
                           │
       ┌───────────────────┼───────────────────┐
       │                   │                   │
       ▼                   ▼                   ▼
┌─────────────┐    ┌─────────────┐    ┌─────────────┐
│  Service    │    │    AI       │    │  Analytics  │
│  Events     │    │  Pipeline   │    │  Events     │
└──────┬──────┘    └──────┬──────┘    └──────┬──────┘
       │                  │                  │
       └──────────────────┼──────────────────┘
                          │
                          ▼
┌──────────────────────────────────────────┐
│         Workflow Engine Service          │
│                                          │
│  ┌────────────┐  ┌────────────────────┐  │
│  │  Trigger   │  │  Workflow Store    │  │
│  │  Matcher   │  │  (PostgreSQL)      │  │
│  └─────┬──────┘  └────────────────────┘  │
│        │                                 │
│        ▼                                 │
│  ┌────────────┐  ┌────────────────────┐  │
│  │ Condition  │  │  Execution Log     │  │
│  │ Evaluator  │  │  (audit trail)     │  │
│  └─────┬──────┘  └────────────────────┘  │
│        │                                 │
│        ▼                                 │
│  ┌────────────┐                          │
│  │  Action    │                          │
│  │  Executor  │                          │
│  └─────┬──────┘                          │
└────────┼─────────────────────────────────┘
         │
         ├────────▶ Commodore (clips, metadata, markers)
         ├────────▶ External (webhooks)
         ├────────▶ Signalman (notifications)
         ├────────▶ Listmonk (email)
         └────────▶ Foghorn (processing jobs)
```

### Storage

Two Commodore tables: `workflows` (tenant-scoped JSONB definitions) and `workflow_executions` (audit trail with trigger event, actions executed, status). GraphQL CRUD + test execution mutation.

## Impact / Dependencies

- **New Service**: Workflow engine (or extension of Commodore)
- **Kafka**: Subscription to `service_events`, `analytics_events`, `ai_events`
- **Commodore Schema**: Workflow tables
- **Bridge**: GraphQL schema additions
- **Webapp**: Visual builder UI
- **Listmonk**: Email action integration
- **Signalman**: Notification action integration
- **AI Pipeline**: Detection events emitted to Kafka (dependency)

## Alternatives Considered

- **Webhook-only approach**: Less flexible, requires tenant infrastructure
- **Third-party workflow tools (Zapier, n8n)**: Adds external dependency, data leaves platform
- **Lambda/serverless functions**: More powerful but harder for non-developers

## Risks & Mitigations

- **Runaway workflows**: Mitigate with rate limits per workflow, circuit breakers
- **Action failures**: Mitigate with retries, dead letter queue, clear error reporting
- **Complex debugging**: Mitigate with detailed execution logs, test mode
- **AI false positives**: Mitigate with confidence thresholds, cooldown periods

## Migration / Rollout

1. Implement workflow engine service
2. Add database schema
3. Add GraphQL API (non-AI triggers first)
4. Build visual builder in webapp
5. Create template library
6. Integrate AI pipeline triggers
7. Beta with select tenants
8. GA with documentation

## Open Questions

- Should workflows be versioned (immutable definitions)?
- How to handle webhook action failures (retry policy)?
- Should we support workflow templates that tenants can fork?
- Max workflows per tenant?
- Which AI models to support initially?
- How to handle AI trigger latency (detection delay)?

## References, Sources & Evidence

- [Reference] https://help.shopify.com/en/manual/shopify-flow
- [Reference] https://shopify.dev/docs/apps/build/flow
- [Evidence] Shopify Flow uses trigger → condition → action model
- [Evidence] Disabled automations page exists in webapp

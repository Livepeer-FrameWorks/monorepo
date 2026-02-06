# Deckhand - Support Messaging System

Deckhand provides native in-app messaging for FrameWorks using Chatwoot as the backend storage and agent dashboard. Users interact with a custom chat UI while support agents use Chatwoot.

### Chatwoot Manual Setup

Chatwoot admin configuration must be done manually after deployment:

1. Create API channel inbox (not website widget)
2. Add custom attributes: `tenant_id`, `page_url`, `subject`
3. Configure webhook URL: `http://deckhand:18015/webhooks/chatwoot`
4. Enable events: `conversation_created`, `conversation_updated`, `message_created`, `message_updated`

Chatwoot requires Redis for background jobs. Chatwoot does not support webhook HMAC ([#9354](https://github.com/chatwoot/chatwoot/issues/9354)) — security relies on Docker network isolation.

## Architecture

```
┌─────────────┐     ┌──────────────┐     ┌───────────┐     ┌───────────┐
│   Webapp    │────▶│   Bridge     │────▶│  Deckhand │────▶│  Chatwoot │
│  /messages  │◀────│  (GraphQL)   │◀────│  (gRPC)   │◀────│  (API)    │
└─────────────┘     └──────────────┘     └─────┬─────┘     └───────────┘
                                               │
                         Real-time replies     ▼
                    ┌───────────┐     ┌───────────────┐
                    │ Signalman │◀────│    Decklog    │
                    │ (WebSocket)│    │ (Kafka ingest)│
                    └───────────┘     └───────────────┘
```

## Why Sidecar Pattern?

Deckhand acts as an adapter between FrameWorks and Chatwoot:

| Concern              | Without Deckhand                     | With Deckhand                  |
| -------------------- | ------------------------------------ | ------------------------------ |
| Chatwoot webhooks    | Bridge receives (violates stateless) | Deckhand receives              |
| Enrichment logic     | Bridge mixes concerns                | Deckhand owns                  |
| Real-time broadcast  | Bridge → Signalman (awkward)         | Deckhand → Decklog → Signalman |
| Chatwoot HTTP client | Lives in Bridge                      | Lives in Deckhand              |

Bridge stays a pure GraphQL gateway. Deckhand owns all Chatwoot integration.

## Components

### Deckhand Service (`api_ticketing/`)

**gRPC Server** - Called by Bridge:

- `ListConversations` - Paginated conversation list for tenant
- `SearchConversations` - Search conversations for tenant
- `GetConversation` - Single conversation by ID
- `CreateConversation` - Start new support thread
- `CreateConversation` enriches Chatwoot contact with tenant name (Quartermaster) and billing email (Purser)
- `ListMessages` - Messages within a conversation
- `SendMessage` - User sends message

**HTTP Webhook Handler** - Called by Chatwoot:

- `POST /webhooks/chatwoot` - Receives `conversation_created`, `conversation_updated`, `message_created`, `message_updated` events

**Chatwoot Client** - Proxies to Chatwoot API:

- Contact management (find/create by tenant_id)
- Conversation CRUD
- Message sending
- Private notes for agent context

### Enrichment

When a conversation is created, Deckhand fetches context and posts a private note:

```
Quartermaster ──▶ tenant name, created date
Purser ──▶ billing email, tier, status
Webhook payload ──▶ page URL
```

Agents see:

```markdown
## Customer Context

**Tenant:** Acme Corp
**Email:** billing@acme.com
**Plan:** Pro (active)
**Member since:** Mar 2024
**Page:** /streams/abc123
```

### Real-Time Flow

When an agent replies in Chatwoot:

1. Chatwoot sends `message_created` webhook to Deckhand
2. Deckhand creates a `ServiceEvent` (`message_received`)
3. Sends to Decklog via `SendServiceEvent()`
4. Decklog publishes to Kafka `service_events`
5. Signalman routes to `CHANNEL_MESSAGING` subscribers
6. WebSocket pushes to user's browser

## Data Model

Proto: `pkg/proto/deckhand.proto`. GraphQL types: `pkg/graphql/schema.graphql` (Conversation, Message, ConversationStatus).

Configuration: See `docker-compose.yml` and `api_ticketing/internal/config/config.go`.

## Signalman Integration

Messaging uses dedicated channel and event type:

```go
// Channel routing (api_realtime/cmd/signalman/main.go)
case "message_received", "message_updated", "conversation_created", "conversation_updated":
    return pb.Channel_CHANNEL_MESSAGING

// Event type mapping
case "message_received", "message_updated", "conversation_created", "conversation_updated":
    return pb.EventType_EVENT_TYPE_MESSAGE_LIFECYCLE
```

Clients subscribe to `CHANNEL_MESSAGING` to receive real-time message updates.

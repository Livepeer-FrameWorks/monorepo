# Deckhand - Support Messaging System

Deckhand provides native in-app messaging for FrameWorks using Chatwoot as the backend storage and agent dashboard. Users interact with a custom chat UI while support agents use Chatwoot.

## Status

| Component | Status |
|-----------|--------|
| Deckhand service (gRPC + webhooks) | ✅ Implemented |
| Chatwoot HTTP client | ✅ Implemented |
| Webhook enrichment (Quartermaster, Purser) | ✅ Implemented |
| Bridge resolvers (queries, mutations) | ✅ Implemented |
| GraphQL types and schema | ✅ Implemented |
| Frontend routes (`/messages`) | ✅ Implemented |
| `ServiceEvent` in ipc.proto | ✅ Implemented |
| `service_events` topic | ✅ Implemented |
| `CHANNEL_MESSAGING` in Signalman | ✅ Implemented |
| Real-time subscription wiring | ✅ Implemented |
| Chatwoot docker-compose setup | ✅ Implemented |
| Chatwoot admin configuration | ⚠️ Manual setup required |

**Note:** Chatwoot requires Redis for background jobs; deploy Redis alongside Chatwoot.

### Manual Setup Required

Chatwoot admin configuration must be done manually after deployment:
1. Create API channel inbox (not website widget)
2. Add custom attributes: `tenant_id`, `page_url`, `subject`
3. Configure webhook URL: `http://deckhand:18015/webhooks/chatwoot`
4. Enable events: `conversation_created`, `conversation_updated`, `message_created`, `message_updated`

> **Note:** Chatwoot does not support webhook signature verification (HMAC).
> See [GitHub Issue #9354](https://github.com/chatwoot/chatwoot/issues/9354).
> Security relies on internal Docker network isolation (webhook endpoint not exposed publicly).

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

| Concern | Without Deckhand | With Deckhand |
|---------|------------------|---------------|
| Chatwoot webhooks | Bridge receives (violates stateless) | Deckhand receives |
| Enrichment logic | Bridge mixes concerns | Deckhand owns |
| Real-time broadcast | Bridge → Signalman (awkward) | Deckhand → Decklog → Signalman |
| Chatwoot HTTP client | Lives in Bridge | Lives in Deckhand |

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

### Proto Messages

```protobuf
// pkg/proto/deckhand.proto
message DeckhandConversation {
  string id = 1;
  string subject = 2;
  ConversationStatus status = 3;
  DeckhandMessage last_message = 4;
  int32 unread_count = 5;
  google.protobuf.Timestamp created_at = 6;
  google.protobuf.Timestamp updated_at = 7;
}

message DeckhandMessage {
  string id = 1;
  string conversation_id = 2;
  string content = 3;
  MessageSender sender = 4;
  google.protobuf.Timestamp created_at = 5;
}
```

### GraphQL Types

```graphql
type Conversation implements Node {
  id: ID!
  subject: String
  status: ConversationStatus!
  lastMessage: Message
  unreadCount: Int!
  createdAt: Time!
  updatedAt: Time!
}

enum ConversationStatus { OPEN RESOLVED PENDING }
enum MessageSender { USER AGENT }
```

## Configuration

### Environment Variables

```env
# Deckhand service
DECKHAND_PORT=18015
DECKHAND_GRPC_PORT=19006
SERVICE_TOKEN=<shared>

# Chatwoot connection
CHATWOOT_HOST=chatwoot
CHATWOOT_PORT=3000
CHATWOOT_ACCOUNT_ID=1
CHATWOOT_INBOX_ID=1
CHATWOOT_API_TOKEN=<secret>
DECKHAND_WEBHOOK_RATE_LIMIT_PER_MIN=600

# gRPC dependencies
QUARTERMASTER_GRPC_ADDR=quartermaster:19002
PURSER_GRPC_ADDR=purser:19003
DECKLOG_GRPC_ADDR=decklog:18006
```

**Note:** `DECKHAND_HOST` is used by config generation to derive `DECKHAND_GRPC_ADDR` for clients (e.g., Bridge). It is not required by the Deckhand binary itself.

### Chatwoot Setup

1. Create API channel inbox (not website widget)
2. Add custom attributes: `tenant_id`, `page_url`, `subject`
3. Configure webhook URL: `http://deckhand:18015/webhooks/chatwoot`
4. Enable events: `conversation_created`, `conversation_updated`, `message_created`, `message_updated`

## File Structure

```
api_ticketing/
├── cmd/deckhand/main.go
├── internal/
│   ├── chatwoot/client.go      # Chatwoot HTTP API
│   ├── grpc/server.go          # gRPC for Bridge
│   └── handlers/
│       ├── handlers.go         # HTTP setup
│       └── webhooks.go         # Webhook + enrichment

pkg/proto/deckhand.proto        # Service contract
pkg/clients/deckhand/           # Bridge client

website_application/src/routes/messages/
├── +page.svelte                # Inbox
└── [id]/+page.svelte           # Thread
```

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

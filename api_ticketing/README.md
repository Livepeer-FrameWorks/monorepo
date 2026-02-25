# Deckhand (Support Messaging)

Native in-app support messaging using Chatwoot as the backend. Users interact via a custom chat UI while support agents use the Chatwoot dashboard. **Tenant-isolated**—all messages are scoped to the authenticated tenant.

## Why Deckhand?

- **Native UX**: Chat UI embedded in the dashboard, no external widget
- **Tenant context**: Enriches messages with billing tier, subscription status, and page context
- **Real-time**: WebSocket delivery via Signalman for instant message updates
- **Audit trail**: All messaging events flow through the service events pipeline

## Ports

| Port  | Protocol | Purpose                 |
| ----- | -------- | ----------------------- |
| 18015 | HTTP     | Webhooks from Chatwoot  |
| 19006 | gRPC     | Internal API for Bridge |

## Configuration

| Env Var                               | Description                                                         |
| ------------------------------------- | ------------------------------------------------------------------- |
| `SERVICE_TOKEN`                       | Service token for internal gRPC auth (Quartermaster/Purser/Decklog) |
| `DECKHAND_PORT`                       | HTTP port for webhook + health endpoints                            |
| `DECKHAND_GRPC_PORT`                  | gRPC port for Bridge integration                                    |
| `DECKHAND_WEBHOOK_RATE_LIMIT_PER_MIN` | Webhook rate limit per minute                                       |
| `CHATWOOT_HOST`                       | Chatwoot host (no scheme)                                           |
| `CHATWOOT_PORT`                       | Chatwoot port                                                       |
| `CHATWOOT_ACCOUNT_ID`                 | Chatwoot account ID                                                 |
| `CHATWOOT_INBOX_ID`                   | API channel inbox ID                                                |
| `CHATWOOT_API_TOKEN`                  | Chatwoot API access token                                           |
| `QUARTERMASTER_GRPC_ADDR`             | Quartermaster gRPC address                                          |
| `PURSER_GRPC_ADDR`                    | Purser gRPC address                                                 |
| `DECKLOG_GRPC_ADDR`                   | Decklog gRPC address                                                |

## Run (dev)

```bash
docker-compose up deckhand
```

## Related

- Architecture: `docs/architecture/deckhand.md`
- GraphQL: Conversation/Message types in `pkg/graphql`
- Real-time: `CHANNEL_MESSAGING` in Signalman

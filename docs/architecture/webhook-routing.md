# Webhook Routing Architecture

External webhooks (from payment providers, etc.) are routed through the API Gateway to internal services via gRPC. This keeps internal services unexposed to the public internet.

## Architecture Overview

```
Payment Provider    API Gateway (public)         Internal Service (mesh only)
     │                    │                              │
     │  POST /webhooks/   │                              │
     │  billing/stripe    │                              │
     ├───────────────────>│                              │
     │                    │  gRPC: ProcessWebhook(       │
     │                    │    provider: "stripe",       │
     │                    │    body: [...],              │
     │                    │    headers: {...}            │
     │                    │  )                           │
     │                    ├─────────────────────────────>│
     │                    │                              │ Verify signature
     │                    │                              │ Process webhook
     │                    │<─────────────────────────────│
     │<───────────────────│                              │
```

## Design Decisions

### Why Route Through Gateway?

1. **Single Public Entry Point**: Only Gateway is exposed to the internet
2. **Internal Services Stay on Mesh**: Purser, Quartermaster, etc. only accept gRPC from trusted mesh services
3. **Consistent Security Model**: All external traffic goes through Gateway auth/rate-limiting infrastructure
4. **Signature Verification Stays Internal**: Webhook secrets never leave the target service

### Why Not Verify Signatures in Gateway?

1. **Secrets Stay Internal**: Each service owns its webhook secrets/credentials (STRIPE_WEBHOOK_SECRET, provider API keys)
2. **Provider-Specific Logic**: Different providers use different verification (Stripe HMAC, Mollie API fetch)
3. **Single Responsibility**: Gateway just routes; services own business logic

## Implementation Details

### Gateway Webhook Router

Location: `api_gateway/internal/webhooks/router.go`

```go
// WebhookRouter routes webhooks to internal services via gRPC
type Router struct {
    handlers map[string]ServiceHandler  // "billing" -> PurserWebhookHandler
    logger   logging.Logger
}

// ServiceHandler interface for services that accept webhooks
type ServiceHandler interface {
    ProcessWebhook(ctx context.Context, req *pb.WebhookRequest) (*pb.WebhookResponse, error)
}
```

Route: `POST /webhooks/:service/:provider`

Examples:

- `POST /webhooks/billing/stripe` → Purser gRPC ProcessWebhook
- `POST /webhooks/billing/mollie` → Purser gRPC ProcessWebhook

### Proto Definition

Location: `pkg/proto/shared.proto`

```protobuf
message WebhookRequest {
  string provider = 1;              // "stripe", "mollie", etc.
  bytes body = 2;                   // Raw HTTP body
  map<string, string> headers = 3;  // All HTTP headers
  string source_ip = 4;             // Client IP for logging
  int64 received_at = 5;            // Unix timestamp
}

message WebhookResponse {
  bool success = 1;
  string error = 2;
  int32 status_code = 3;            // HTTP status to return
}
```

### Service Implementation

Location: `api_billing/internal/grpc/server.go`

```go
func (s *PurserServer) ProcessWebhook(ctx context.Context, req *pb.WebhookRequest) (*pb.WebhookResponse, error) {
    switch req.Provider {
    case "stripe":
        return s.processStripeWebhook(req)
    case "mollie":
        return s.processMollieWebhook(req)
    default:
        return &pb.WebhookResponse{Success: false, Error: "unknown provider", StatusCode: 400}, nil
    }
}
```

## Adding Webhooks for a New Service

1. **Define WebhookService in Proto**

   Add to your service's proto file:

   ```protobuf
   import "shared.proto";

   service WebhookService {
     rpc ProcessWebhook(shared.WebhookRequest) returns (shared.WebhookResponse);
   }
   ```

2. **Implement ProcessWebhook**

   In your gRPC server:

   ```go
   func (s *MyServer) ProcessWebhook(ctx context.Context, req *pb.WebhookRequest) (*pb.WebhookResponse, error) {
       // Verify signature using req.Headers and req.Body
       // Process webhook
       // Return appropriate status
   }
   ```

3. **Register with Gateway**

   In `api_gateway/cmd/bridge/main.go`:

   ```go
   webhookRouter.RegisterService("myservice", serviceClients.MyService)
   ```

4. **Configure Webhook URL with Provider**

   Use: `https://your-gateway-domain.com/webhooks/myservice/provider-name`

## Supported Providers

### Stripe (`/webhooks/billing/stripe`)

Headers used for signature verification:

- `Stripe-Signature`: HMAC signature

Events handled:

- `checkout.session.completed` - Subscription created
- `customer.subscription.*` - Status changes/cancellations
- `invoice.paid` - Payment confirmed
- `invoice.payment_failed` - Payment failed (dunning)
- `payment_intent.succeeded` - Payment confirmed
- `payment_intent.payment_failed` - Payment failed

### Mollie (`/webhooks/billing/mollie`)

Mollie doesn't sign webhooks by default. Verification is done by:

1. Fetching the payment/subscription from Mollie API

Events handled:

- Payment status changes (paid, failed, expired)
- Subscription status changes
- Mandate changes

## Idempotency

Webhook events are tracked in `purser.webhook_events`:

```sql
CREATE TABLE purser.webhook_events (
    id UUID PRIMARY KEY,
    provider VARCHAR(50) NOT NULL,
    event_id VARCHAR(255) NOT NULL,
    event_type VARCHAR(100),
    processed_at TIMESTAMPTZ DEFAULT NOW(),
    UNIQUE(provider, event_id)
);
```

Before processing, check if event was already handled:

```go
// Check idempotency
exists, err := s.checkWebhookEventExists(ctx, provider, eventID)
if exists {
    return &pb.WebhookResponse{Success: true, StatusCode: 200}, nil // Already processed
}
```

## Testing Webhooks

### Stripe CLI

```bash
# Forward webhooks to local gateway
stripe listen --forward-to localhost:18000/webhooks/billing/stripe

# Trigger test events
stripe trigger checkout.session.completed
```

### Mollie

1. Use ngrok to expose local gateway
2. Configure webhook URL in Mollie dashboard
3. Use Mollie test mode for sandbox transactions

## Security Considerations

1. **No Authentication Middleware**: Webhook routes skip JWT auth (providers can't authenticate)
2. **Verification Required**: Stripe signatures require `STRIPE_WEBHOOK_SECRET`; Mollie signatures are verified when `MOLLIE_WEBHOOK_SECRET` is set, otherwise we confirm by fetching from the Mollie API
3. **Log Source IP**: Track source IPs for debugging and potential IP allowlisting
4. **Rate Limiting**: Gateway enforces per-IP rate limits on webhook routes
5. **Payload Limits**: Gateway rejects webhook payloads larger than 1MB
6. **Timeout Handling**: Webhooks have short timeouts; process quickly or queue for async

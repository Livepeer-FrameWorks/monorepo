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

Webhook events are deduplicated via `purser.webhook_events` (keyed on `provider` + `event_id`). Schema: `pkg/database/sql/schema/purser.sql`.

**Security**: Webhook routes skip JWT auth (providers can't authenticate). Signature verification happens in the target service, not Gateway. Gateway enforces per-IP rate limits and rejects payloads >1MB.

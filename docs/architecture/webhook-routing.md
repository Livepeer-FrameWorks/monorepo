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
     │                    │                              │ Verify/validate
     │                    │                              │ Persist inbox row
	 │                    │<─────────────────────────────│
	 │<───────────────────│                              │
     │                    │                              │ Background worker
     │                    │                              │ leases + reconciles
```

## Implementation Details

### Gateway Webhook Router

Location: `api_gateway/internal/webhooks`

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

Location: `pkg/proto/shared.proto` (generated Go package `pkg/proto/shared`)

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

Location: `api_billing/internal/grpc`

```go
func (s *PurserServer) ProcessWebhook(ctx context.Context, req *pb.WebhookRequest) (*pb.WebhookResponse, error) {
    switch req.Provider {
    case "stripe":
        return handlers.ProcessStripeWebhookGRPC(req.Body, req.Headers)
    case "mollie":
        return handlers.ProcessMollieWebhookGRPC(req.Body, req.Headers)
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

Mollie doesn't sign webhook bodies by default. The ingress validates the form
payload and durably records it; the worker verifies the referenced object by:

1. Fetching the payment/subscription from Mollie API

Events handled:

- Payment status changes (paid, failed, expired)
- Subscription status changes
- Mandate changes

## Idempotency

Verified/validated deliveries first enter `purser.provider_webhook_inbox`.
Purser returns 2xx only after that insert commits. Workers lease inbox rows with
token fencing and exponential backoff, so provider HTTP latency is independent
of reconciliation and a worker crash leaves recoverable work. Stripe event IDs
deduplicate ingress. Mollie deliveries use unique receipt keys because one
payment ID may legitimately report multiple states; authoritative payment-state
event IDs deduplicate reconciliation.

Reconciled provider events are recorded in `purser.webhook_events` (keyed on
`provider` + `event_id`). Schema: `pkg/database/sql/schema`.

Current provider behavior:

- Stripe claims `purser.webhook_events` before applying a state transition and
  skips terminal duplicate event IDs.
- Mollie fetches the payment from Mollie, derives an ID from the authoritative
  payment state, and claims that ID before applying the transition.
- Unknown Mollie payment IDs are acknowledged with 200 and retired from the
  inbox; the response does not disclose whether a provider object exists.

**Security**: Webhook routes skip JWT auth (providers can't authenticate). Signature verification happens in the target service, not Gateway. Gateway enforces per-IP rate limits and rejects payloads >1MB.

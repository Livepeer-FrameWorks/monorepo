# Bosun (Outbound Webhooks)

Bosun (`api_webhooks`, binary `bosun`) delivers the public tenant events to tenant-configured
HTTPS endpoints. It owns the endpoints, their signing secrets, the stored events, the delivery
ledger with every HTTP attempt, and replay. The events it delivers are the public types of
`pkg/proto/events/public/v1`, which their owning services write to `domain.events` through
transactional outboxes (see `service-events.md`); Bosun performs no mapping of its own.

```
owning service ─ outbox ─→ Decklog ─→ Kafka domain.events (+ mirrored copies)
                                                │ durable group "bosun", aggregator region
                                                ▼
                              ce_visibility != public → drop before decode
                              registry check, tenant required, undecodable → DLQ
                                                │ one transaction
                                                ▼
     Postgres `bosun`: webhook_events (UNIQUE event_id) + one webhook_deliveries row per subscribed endpoint
                                                │ offset commits after the commit
                                                ▼
     workers: claim due rows (SKIP LOCKED, 30 s lease, ≤5 in flight per endpoint)
              → render {id,type,api_version,created_at,data} → sign (Standard Webhooks)
              → POST through the destination-policy dialer → settle (fenced on lease token)

Bridge GraphQL / MCP ─ gRPC bosun.BosunService ─→ endpoints, deliveries, replay, test, rotate
```

## Service

| Item         | Value                                                                                       |
| ------------ | ------------------------------------------------------------------------------------------- |
| Module       | `frameworks/api_webhooks`, entrypoint `cmd/bosun`                                           |
| HTTP         | `BOSUN_PORT` (18013): `/health`, `/ready`, `/metrics`                                       |
| gRPC         | `BOSUN_GRPC_PORT` (19009): `bosun.BosunService`, gRPC health                                |
| DNS          | `bosun.internal`; Bridge reads `BOSUN_GRPC_ADDR` (optional; empty disables the webhook API) |
| Placement    | Replicas in the aggregator region only                                                      |
| Database     | PostgreSQL database `bosun`, baseline `pkg/database/sql/schema/bosun.sql`                   |
| Kafka        | Aggregator cluster: consumes `domain.events` and `<prefix>.domain.events` per mirror prefix |
| Dependencies | Quartermaster (bootstrap), Purser (billing contact email), Decklog (audit events), SMTP     |
| Secrets      | `BOSUN_FIELD_ENCRYPTION_KEY`: Bosun's own field keyring; no other service holds it          |
| Client       | `pkg/clients/bosun`; typed config in `api_webhooks/internal/appconfig`                      |

The operator configuration reference lists every key. Bosun has no knobs for delivery behaviour:
the limits below are constants in `internal/ledger` and `internal/delivery`.

## Tenant boundary

Every RPC acts on the tenant of the call's auth context: a forwarded user JWT, or the service
token with `x-tenant-id` metadata. No request field names a tenant. A call without a tenant
context is `PERMISSION_DENIED`; an ID of another tenant's endpoint or delivery is `NOT_FOUND`.
Every table is keyed by `tenant_id` and every query filters by it.

Bosun also authorizes every write, including secret rotation, test sends and replays. User actors
must pass `ActionManageWebhooks` (tenant owner/admin or platform operator); API-token actors also
need `developer:write`. A validated internal service token is trusted, but user-supplied metadata
cannot turn a session JWT into a service call. Bridge performs the same checks before forwarding.

gRPC codes that Bridge maps to GraphQL unions: `INVALID_ARGUMENT` (input validation) and
`FAILED_PRECONDITION` (the 10-endpoint limit, replaying a pending or test delivery, replaying on a
disabled endpoint) become `ValidationError`; `RESOURCE_EXHAUSTED` (a test within 10 seconds of the
previous one) becomes `RateLimitError`; `NOT_FOUND` becomes `NotFoundError`; `UNAVAILABLE` means
the endpoint host could not be resolved at validation time.

## Data model

| Table                         | Holds                                                                                     |
| ----------------------------- | ----------------------------------------------------------------------------------------- |
| `webhook_tenants`             | Per-tenant endpoint count, `CHECK (endpoint_count BETWEEN 0 AND 10)`                      |
| `webhook_endpoints`           | URL, event types (`*` for all), pinned `api_version` (`v1`), status, failure streak       |
| `webhook_endpoint_secrets`    | Field-encrypted keys; one `active`, at most one unexpired `previous`                      |
| `webhook_events`              | Public payload bytes and schema name, `UNIQUE (event_id)` (the emitter's `ce_id`)         |
| `webhook_deliveries`          | Status, attempts, lease token and expiry, next attempt, replay count; kind `event`/`test` |
| `webhook_delivery_attempts`   | Status code, error class, latency, response excerpt (at most 1,024 bytes)                 |
| `webhook_notification_outbox` | Auto-disable emails to the billing contact                                                |
| `domain_event_outbox`         | Bosun's own internal audit events (`webhook.endpoint_auto_disabled`), relayed to Decklog  |

The database is created from the baseline in the release that introduced it, so it has no
migrations of its own yet.

## Consume and fan out

- The consumer group `bosun` is durable and shared by all replicas, so each record is handled
  once and offsets survive restarts.
- A record whose visibility header is not public is dropped before its payload is decoded. A type
  this binary does not register is dropped and logged at error level, so Bosun must be upgraded
  before a producer emits a new public type. A registered internal type under a public header and
  a public event without a tenant are dropped and counted. An undecodable payload goes to the
  dead-letter topic.
- `RecordEvent` stores the event and one delivery for each enabled endpoint of the tenant that
  subscribes to the type and whose `created_at` is at or before the event time, in one
  transaction. An event no endpoint receives is not stored. `UNIQUE (event_id)` absorbs the
  mirrored copies and Kafka redelivery.
- A database failure retries in place with backoff up to 30 seconds; the offset commits only after
  the transaction commits, so later offsets of the partition never pass an unstored record.
- The creation boundary compares the emitter's event time with Bosun's database clock, so it is
  approximate by the clock difference between them, about a second.

## Delivery

- **Claim:** workers claim due deliveries with `FOR UPDATE SKIP LOCKED`, at most 5 leased per
  endpoint, 32 sends per replica. A lease lasts 30 seconds and carries a token; a send that could
  not finish 10 seconds plus a 5-second margin before the lease ends is not started, and
  settlement is fenced on the token, so a reclaimed lease never has two senders.
- **Render:** `{id, type, api_version, created_at, data}` with `data` rendered by protojson
  (`EmitUnpopulated`, lowerCamelCase names, 64-bit integers as strings). `id` and `webhook-id` are
  the event ID, identical across endpoints, retries, and replays.
- **Sign:** `pkg/webhooksig`, the Standard Webhooks scheme: `v1,<base64 HMAC-SHA256>` over
  `<id>.<unix seconds>.<body>`, one signature per live secret.
- **Send:** a client with no proxy, no redirects, no connection reuse, TLS 1.2 or later, a
  10-second limit for the whole attempt, and at most 4 KiB of the response read. The destination
  policy (`pkg/restream.WebhookDestinationPolicy`) and the URL rules
  (`restream.ValidateWebhookURL`, shared with playback-auth webhooks in Commodore and Foghorn)
  validate the URL at creation, and the policy runs again in the dialer's `Control` hook on the
  resolved address of every connection, which is the DNS-rebinding defense. It allows public
  destinations only, unless `BOSUN_ALLOW_PRIVATE_DESTINATIONS` is set. That setting is for
  isolated staging and development clusters: it also admits RFC 1918 / ULA addresses, plain
  `http`, and `.local`/`.internal` names, while loopback, link-local, metadata, and
  `*.frameworks.network` stay refused. The cluster manifest's
  `webhooks.allow_private_destinations` renders it together with
  `PLAYBACK_WEBHOOK_ALLOW_PRIVATE_DESTINATIONS` on Commodore and Foghorn. Dev compose enables it. A 2xx is success; any other
  status, including 3xx, is a failure.
- **Retry:** 30 s, 2 min, 10 min, 30 min, 1 h, 2 h, 4 h, 8 h, 12 h, 24 h, 24 h, each ±10 percent;
  12 attempts over about 3.2 days, then `failed`.
- **Bosun-side failure:** a delivery Bosun cannot send for a reason of its own settles with
  error class `internal`, releases its lease, writes no attempt, and leaves the endpoint's failure
  streak alone (`Store.SettleInternal`). A permanent cause (a signing secret that does not decrypt
  or parse, a stored event that does not decode or match its registered schema) fails the delivery
  at once; the tenant can replay it after the cause is fixed. A transient cause (a database error
  reading secrets, an event type or API version newer than this replica) keeps it pending and
  retries after the delay of its next attempt without spending one. If the settlement itself
  fails, the delivery is reclaimed when its lease expires.
- **Auto-disable:** when an endpoint reaches 20 consecutive failed attempts and the first of them
  is at least 5 days old, the settling transaction disables it (reason `failing`), skips its
  pending deliveries, queues the billing contact email, and enqueues the audit event. Enabling
  resets the streak and resends nothing.
- **Replay:** a single finished event delivery, or up to 1,000 failed or skipped event deliveries
  of one endpoint in a creation-time range, return to pending with the retry schedule restarted.
  The endpoint must be enabled.
- **Test:** a synchronous `webhook.test` delivery, at most once per endpoint per 10 seconds across
  replicas, recorded as a finished delivery of kind `test`.
- **Secrets:** 32 random bytes, returned as `whsec_<base64>` only by create and rotate. Rotation
  keeps the previous secret signing for 24 hours unless it is revoked immediately.

## Retention

A maintenance loop prunes hourly in batches: events older than 30 days (with their deliveries and
attempts) unless a delivery is still pending, test deliveries older than 30 days, sent
notification emails older than 30 days, and expired previous secrets. An event batch takes the
same locks in the same order as a replay: it picks events with no pending delivery, locks their
endpoints (`FOR SHARE`), and deletes only those that still have none, so a replay that committed
first keeps its event and one that waited finds its delivery gone (not found).

## Metrics and alerts

Metrics carry no tenant labels: `bosun_domain_events_total{result}`,
`bosun_deliveries_created_total`, `bosun_delivery_attempts_total{kind,outcome,error_class}`,
`bosun_delivery_attempt_duration_seconds`, `bosun_deliveries_finished_total{status}`,
`bosun_endpoints_auto_disabled_total`, `bosun_internal_errors_total{stage}`,
`bosun_pruned_rows_total{table}`, `bosun_oldest_due_delivery_seconds`,
`bosun_replays_total{mode}`, and the consumer lag gauge.

Alerts in `pkg/grafana/rules/frameworks.yml`: `BosunWebhookDeliveryBacklog` (oldest due delivery
over 15 minutes), `BosunDomainEventConsumerLag` (over 1,000 records on a topic), and
`BosunInternalDeliveryFailures` (Bosun-side failures that left claimed deliveries unsent).

## Surfaces

- GraphQL (Bridge): `webhookEndpoint`, `webhookEndpointsConnection`,
  `webhookDeliveriesConnection`, `webhookDelivery` (the only read that loads `attemptHistory`),
  `webhookEventTypes`, and the create, update, delete, enable, disable, rotate, test, replay, and
  range-replay mutations. The secret is only on `WebhookEndpointSecret`, the result of create and
  rotate.
- MCP: read tools `list_webhook_endpoints`, `get_webhook_endpoint`, `list_webhook_deliveries`,
  `get_webhook_delivery`, `list_webhook_event_types`; write tools that require a confirmation
  string for create, update, delete, enable, disable, secret rotation, test, and both replays.
- Webapp: `/developer/webhooks` and `/developer/webhooks/[id]`.
- Tenant docs: `website_docs/src/content/docs/builders/webhooks.mdx`.

## Tests

- Unit tests (`make test-bosun`, plus `pkg/webhooksig`): URL, event type, and description
  validation; Standard Webhooks signer vectors; body rendering; consumer dispositions.
- `make verify-bosun-db` (`BOSUN_REALPG_TESTS` on PostgreSQL in Docker): mirrored duplicates,
  tenant isolation, lease reclaim without double delivery, backoff and auto-disable, replay ID,
  pruning, prune racing a replay, permanent and transient Bosun-side failures, the endpoint limit
  and secrets, offsets committed after the transaction (franz-go
  `kfake`), and delivery to a local TLS receiver through a client pinned to that address.

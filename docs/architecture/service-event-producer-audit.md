# Service-Event Producer Audit

This document classifies every Decklog producer call site in the monorepo as **state-coupled** (durable transactional outbox required) or **loss-tolerant telemetry** (fire-and-forget acceptable). It also names the canonical migration pattern for state-coupled producers.

Regional Decklog/Kafka topologies mean state-coupled events that aren't durably enqueued can be lost during a regional Decklog outage. This audit is the prerequisite for converting those producers to outbox-backed emission.

## Classification rules

- **State-coupled**: the event reflects a transactional state change (a row was written/updated/deleted) and a consumer somewhere relies on the event arriving exactly once. Loss = state divergence. Examples: billing invoice creation, plan-tier change, cluster grant, stream policy revocation, artifact lifecycle (clip created/ended/deleted).
- **Loss-tolerant telemetry**: the event is observational and a consumer can survive missing one. Loss = a gap in a graph. Examples: API request counters, heartbeats, mist triggers (replayed from MistServer state), routing decisions (aggregated stats).

The line between the two is blunt by design — when in doubt, default to state-coupled.

## Canonical migration pattern

The reference is `commodore.playback_policy_invalidation_outbox` + `api_control/internal/grpc/invalidation_outbox.go` (now generalized to use [pkg/outbox](../../pkg/outbox/outbox.go)). The shape:

1. The state mutation and the outbox INSERT share a single SQL transaction. A failed enqueue rolls the mutation back: **no durability, no mutation**.
2. A per-row `attempts` + `next_attempt_at` lease pattern lets the worker run on every replica without leader election (`SELECT ... FOR UPDATE SKIP LOCKED` + lease-window push).
3. Exponential backoff capped at a max; no terminal abandon (a partitioned consumer catches up when it returns).
4. Alert threshold logs the row at `ERROR` past `AlertAfterAttempts` so on-call gets paged.
5. The `outbox.Worker[Payload]` orchestration in `pkg/outbox` handles poll loop, dispatch, backoff, alert; per-service `Store[Payload]` + `Dispatcher[Payload]` adapters carry the table-specific SQL and the actual delivery (here: Decklog SendServiceEvent).

Per-service outbox table convention:

```sql
CREATE TABLE <service>.service_event_outbox (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id UUID NOT NULL,
    event_type TEXT NOT NULL,
    -- ServiceEvent proto serialized as protojson; Decklog re-parses on drain
    payload JSONB NOT NULL,
    status TEXT NOT NULL DEFAULT 'pending',
    attempts INT NOT NULL DEFAULT 0,
    next_attempt_at TIMESTAMP NOT NULL DEFAULT NOW(),
    last_error TEXT,
    created_at TIMESTAMP NOT NULL DEFAULT NOW(),
    completed_at TIMESTAMP
);
CREATE INDEX idx_<service>_service_event_outbox_pending
    ON <service>.service_event_outbox(next_attempt_at) WHERE status = 'pending';
```

The producer calls `enqueueServiceEvent(tx, ev)` inside the same transaction as the mutation; a `pkg/outbox.Worker` configured with a `Store` over this table drains to `decklogClient.SendServiceEvent`.

## Producer call-site inventory

Counts derive from a `grep` of `decklogClient.Send*` / `decklog.Emit*` / `SendServiceEvent` / `SendEvent` / `SendGatewayTelemetry` / `Send{Clip,DVR,Vod,Federation,LoadBalancing}*` / `SendTrigger` across production source paths (test files excluded). 45 call sites total at audit time.

### Bridge (`api_gateway`)

| Site                                       | Event class                 | Classification | Migration target                                                                                                                              |
| ------------------------------------------ | --------------------------- | -------------- | --------------------------------------------------------------------------------------------------------------------------------------------- |
| `internal/middleware/usage_tracker.go:311` | API usage counters          | **telemetry**  | Stay fire-and-forget. Lossy under outage is acceptable; usage is reconstructible from `api_requests` ClickHouse table.                        |
| `internal/resolvers/api_events.go:119`     | API audit / activity events | **telemetry**  | Stay fire-and-forget. Audit gaps are a known limitation; durable audit is a separate concern with stronger guarantees (compliance audit log). |

### Commodore (`api_control`)

| Site                                     | Event class                                      | Classification    | Migration target                                                                                                                                     |
| ---------------------------------------- | ------------------------------------------------ | ----------------- | ---------------------------------------------------------------------------------------------------------------------------------------------------- |
| `internal/grpc/server.go:5588`           | `ServiceEvent` (auth/stream/key/artifact change) | **state-coupled** | Migrate to `commodore.service_event_outbox`. Most events here mirror a `commodore.streams`/`commodore.signing_keys`/`commodore.clips`/etc. mutation. |
| `internal/grpc/invalidation_outbox.go:*` | Playback-policy invalidation fanout              | **state-coupled** | **Already migrated.** Uses `commodore.playback_policy_invalidation_outbox` + `pkg/outbox.Worker`. Canonical example.                                 |

### Quartermaster (`api_tenants`)

| Site                           | Event class                                 | Classification    | Migration target                                                                                                                                                                                                    |
| ------------------------------ | ------------------------------------------- | ----------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `internal/grpc/server.go:7379` | `ServiceEvent` (tenant/cluster/peer change) | **state-coupled** | Migrate to `quartermaster.service_event_outbox`. Tenant signup/cluster creation/cluster invite changes are state mutations whose consumers (Commodore route cache, Foghorn admission caches) must see every change. |

### Purser (`api_billing`)

| Site                             | Event class                     | Classification    | Migration target                                                                                                                                                                                                           |
| -------------------------------- | ------------------------------- | ----------------- | -------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `internal/grpc/server.go:4564`   | `ServiceEvent` (billing event)  | **state-coupled** | Migrate to `purser.service_event_outbox`. Plan changes, invoice transitions, refund/clawback events all require durability.                                                                                                |
| `internal/handlers/events.go:43` | Payment-provider webhook ingest | **state-coupled** | Migrate. Webhook receipts are the only signal for some provider state transitions; loss = silent payment-state divergence. Note Stripe meter ingestion already uses a Stripe-specific outbox; this is a different surface. |

### Foghorn (`api_balancing`)

| Site                                                                                                     | Event class                                    | Classification    | Migration target                                                                                                                                                                                                                                     |
| -------------------------------------------------------------------------------------------------------- | ---------------------------------------------- | ----------------- | ---------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `internal/grpc/server.go:418, 749, 844, 862, 1220, 1322, 1369, 2535, 2761, 2832, 2917, 3228, 3517, 3612` | Clip / DVR / VOD lifecycle                     | **state-coupled** | Migrate to `foghorn.service_event_outbox` next to the matching `foghorn.artifacts` row mutation. Lifecycle events drive Commodore registry updates, billing meters, and tenant UI; loss = drift between Foghorn's view of artifacts and Commodore's. |
| `internal/grpc/vod_pipeline.go:152, 179`                                                                 | VOD pipeline lifecycle                         | **state-coupled** | Same as above.                                                                                                                                                                                                                                       |
| `internal/jobs/retention.go:298, 387, 442`                                                               | Retention-job-driven deletion lifecycle        | **state-coupled** | Same as above. Retention deletes are real state changes.                                                                                                                                                                                             |
| `internal/handlers/handlers.go:339, 405, 550, 626, 1949`                                                 | Clip/DVR/Federation lifecycle in HTTP handlers | **state-coupled** | Same as above.                                                                                                                                                                                                                                       |
| `internal/handlers/handlers.go:1969`                                                                     | Load-balancing telemetry                       | **telemetry**     | Stay fire-and-forget.                                                                                                                                                                                                                                |
| `internal/control/server.go:4428`                                                                        | VOD lifecycle (control-plane)                  | **state-coupled** | Same artifact-lifecycle migration as the others.                                                                                                                                                                                                     |
| `internal/triggers/processor.go:768`                                                                     | Mist trigger forwarding                        | **telemetry**     | Stay fire-and-forget. MistServer replays trigger state from its own internal state on reconnect; lost triggers are recovered.                                                                                                                        |
| `internal/federation/peer_manager.go:186`                                                                | Federation events                              | **telemetry**     | Stay fire-and-forget. PeerChannel re-advertises on heartbeat; the federation_events table tolerates gaps.                                                                                                                                            |

### Skipper (`api_consultant`)

| Site                                     | Event class                    | Classification | Migration target                                                           |
| ---------------------------------------- | ------------------------------ | -------------- | -------------------------------------------------------------------------- |
| `internal/notify/websocket.go:57`        | WebSocket notification echo    | **telemetry**  | Stay fire-and-forget.                                                      |
| `internal/heartbeat/agent.go:487`        | Heartbeat                      | **telemetry**  | Stay fire-and-forget.                                                      |
| `internal/skipper/decklog_adapter.go:75` | Skipper chat/diagnostic events | **telemetry**  | Stay fire-and-forget unless an event drives a tenant-visible state change. |

### Deckhand (`api_ticketing`)

| Site                                               | Event class             | Classification    | Migration target                                                                                                                                                                                                                                                                                                                                    |
| -------------------------------------------------- | ----------------------- | ----------------- | --------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `internal/handlers/webhooks.go:270, 357, 399, 448` | Chatwoot webhook fanout | **state-coupled** | Routes through `Quartermaster.EnqueueServiceEvent` so the event lands in `quartermaster.service_event_outbox` and the drain worker dispatches to Decklog. Deckhand is stateless (no local DB), so it borrows QM's outbox surface rather than standing up a private schema; the dispatcher reads `event.source = "deckhand"` to attribute correctly. |

## Tally

- **45** total producer call sites at audit time.
- **30** state-coupled (Commodore 1, Quartermaster 1, Purser 2, Foghorn 22, Deckhand 4) requiring outbox migration; one (Commodore invalidation outbox) already migrated.
- **15** loss-tolerant telemetry; remain fire-and-forget.

## Envelope v2 stamping at producer time

Every migrated producer must populate envelope v2 fields (`source_region`, `source_cluster_id`, `stream_origin_region`, `stream_origin_cluster_id`, `event_id` as UUIDv7, `schema_version`) before enqueuing the event into its service outbox. Decklog backfills any leftover empty envelope fields from its own instance identity (`REGION_ID` + `CLUSTER_ID`); producer stamping wins.

## Locality-correct Decklog target

Each producer's `decklogClient` should resolve to:

- **`regional` for telemetry**: nearest regional Decklog. Latency-sensitive, gap-tolerant. Today most producers dial whichever Decklog is wired at construction time; once Quartermaster service-discovery returns a regional Decklog the call sites pick it up without code changes.
- **`tenant_home` for state mutations**: the Decklog in the tenant's home region. State events must order against tenant control writes (Commodore tenant-home writer); writing to a non-home Decklog breaks ordering.

The outbox migration is independent of the locality contract — moving a producer onto the outbox is valuable even before per-region Decklog exists, because it guarantees durability through any Decklog restart.

## Per-service outbox shape

Each migrated service ships: schema + migration file, an enqueue helper, a `pkg/outbox.Worker` drainer wired into the service's gRPC server, and the replacement of every `decklogClient.Send*` call with the enqueue helper inside the existing transaction.

| Service                 | Outbox table                                       | Drain worker entry point                                              |
| ----------------------- | -------------------------------------------------- | --------------------------------------------------------------------- |
| Commodore               | `commodore.service_event_outbox`                   | `runServiceEventOutboxWorker` in `api_control/internal/grpc`          |
| Quartermaster           | `quartermaster.service_event_outbox`               | `runServiceEventOutboxWorker` in `api_tenants/internal/grpc`          |
| Quartermaster (BYO DNS) | `quartermaster.navigator_custom_domain_outbox`     | `runNavigatorCustomDomainOutboxWorker`                                |
| Purser                  | `purser.billing_event_outbox`                      | `runBillingOutboxWorker` in `api_billing/internal/grpc`               |
| Foghorn (artifacts)     | `foghorn.artifact_event_outbox`                    | `artifactoutbox.RunWorker` in `api_balancing/internal/artifactoutbox` |
| Deckhand                | Routes through `Quartermaster.EnqueueServiceEvent` | (drains via QM's `service_event_outbox` worker)                       |

Deckhand is stateless (no local DB) and reuses Quartermaster's `service_event_outbox` via the `EnqueueServiceEvent` RPC; the row's `event.source = "deckhand"` carries originator identity into the dispatcher.

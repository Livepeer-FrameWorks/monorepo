# Decklog - Event Ingress Gateway

Decklog (`api_firehose`) is the single gRPC ingress gateway between event producers and Kafka. Every analytics and service-plane event enters the data plane through it: Decklog validates tenant attribution, derives canonical event types and IDs, stamps envelope identity, and publishes to Kafka. It is a Kafka producer only — no database, no consumers, no read API.

## Architecture

```
Foghorn ─────────────────┐ SendEvent (MistTrigger)
Bridge / Commodore /     │                                    ┌→ analytics_events
Quartermaster / Purser / ├ SendServiceEvent (ServiceEvent)    │
Deckhand / Foghorn ──────┤                        Decklog ────┼→ service_events
                         │                                    │
Livepeer gateways ───────┘ SendGatewayTelemetry               └→ analytics.raw_mist_triggers
```

Three unary RPCs on `DecklogService` (`pkg/proto`):

| RPC                    | Payload                                | Published to                                          | Producers                                                                                            |
| ---------------------- | -------------------------------------- | ----------------------------------------------------- | ---------------------------------------------------------------------------------------------------- |
| `SendEvent`            | `MistTrigger` envelope (oneof payload) | `analytics_events` (+ raw journal for final triggers) | Foghorn (enriched media-plane triggers, lifecycle polls, playback beacons)                           |
| `SendServiceEvent`     | `ServiceEvent` (typed oneof payloads)  | `service_events`                                      | Bridge, Commodore, Quartermaster, Purser, Deckhand, Foghorn ([service-events.md](service-events.md)) |
| `SendGatewayTelemetry` | `GatewayTelemetryEvent`                | `analytics_events`                                    | Livepeer gateways (orchestrator discovery/state/transcode/AI)                                        |

Transport is gRPC-only: TLS plus service-token auth via the shared interceptor (`pkg/middleware`), default port 18006. Success is **ack-after-Kafka** — an RPC returns OK only after the Kafka publish commits. That property is what Helmsman's trigger WAL and Foghorn's `MistTriggerAck` rely on: a positive ack means the trigger is durably ingested. See [trigger-durability.md](trigger-durability.md).

## MistTrigger unwrap → typed event

`SendEvent` accepts a single `MistTrigger` envelope whose oneof payload carries the typed proto (viewer connect/disconnect, stream buffer/end, lifecycle updates, clip/DVR/VOD/storage lifecycle, playback boot/session QoE, federation, load balancing, …). The unwrap (`unwrapMistTrigger` in `api_firehose/internal/grpc/server.go`) maps the oneof case to the canonical Kafka `event_type` string — the same strings Periscope Ingest routes on ([analytics-pipeline.md](analytics-pipeline.md) §4) — and pulls tenant attribution from the payload where the payload carries its own `tenant_id` (poller-shaped payloads do; webhook-shaped triggers use the envelope's).

The Kafka record is the transparent `protojson` serialization of the protobuf message (proto field names, unpopulated fields omitted) in the envelope's `Data` map — no hand-maintained JSON mapping. Consumers get exactly what the proto says.

## Tenant attribution is fail-closed

Decklog rejects any event without a valid UUID `tenant_id` — there is no zero-UUID fallback tenant, and an un-attributed event never reaches Kafka. Enrichment is upstream's job (Foghorn for media-plane triggers); rejections are counted on `events_ingested_total{status="tenant_rejected"|"tenant_missing"}`.

Gateway telemetry has a two-tenant model: `cluster_owner_tenant_id` is always required (discovery/state events are attributed to the cluster owner), and transcode/AI outcome events additionally require `stream_tenant_id`, which becomes the effective event tenant while cluster-owner identity stays in the payload for dual-attribution joins.

## Deterministic event_id

For durable Mist triggers, the typed Kafka `event_id` must be stable across retries so Periscope's UUID-keyed fact tables dedupe replays. Precedence in `stableMistTriggerEventID`:

1. A producer-stamped UUID `event_id` on the trigger is respected as-is (Helmsman stamps it on the durable path; `pkg/clients/decklog` never overwrites a pre-set `EventId`).
2. Otherwise, if the trigger carries `request_id` — the WAL's `source_event_id = sha256(node_id || NUL || trigger_type || NUL || payload_raw)` — Decklog derives a deterministic UUID from `"frameworks:mist-trigger:" + source_event_id`.
3. Only best-effort triggers with neither fall back to a random UUID.

The derivation contract lives in [trigger-durability.md](trigger-durability.md); the effect is that Helmsman WAL retries and Kafka redeliveries collapse downstream instead of double-inserting facts.

## Raw-trigger audit republish

For seven accounting trigger types (`USER_END`, `STREAM_END`, `PUSH_END`, `RECORDING_END`, `RECORDING_SEGMENT`, `LIVEPEER_SEGMENT_COMPLETE`, `PROCESS_AV_VIRTUAL_SEGMENT_COMPLETE` — a proper subset of Foghorn's eight-trigger durable-ack set; `PUSH_INPUT_CLOSE` is durable but not journaled), `SendEvent` additionally republishes the **original marshaled `MistTrigger` protobuf** to the audit topic `analytics.raw_mist_triggers` (`DECKLOG_RAW_TRIGGERS_TOPIC`; `-` disables). Periscope consumes it into the `raw_mist_triggers` journal for incident recovery and reparse.

- Records are keyed by `source_event_id` with `trigger_type` / `node_id` / `source_event_id` / `tenant_id` headers.
- A final trigger without a `source_event_id` **fails the RPC** — an unkeyed accounting fact cannot be deduped downstream, so Decklog refuses to ack it.
- A raw-journal publish failure also fails the RPC, so Helmsman's WAL retries the whole trigger. The typed publish that already succeeded is safe to repeat because the typed `event_id` is deterministic.

## Envelope region/cluster backfill

Events carry envelope v2 identity: `source_region` / `source_cluster_id` (where the event was produced) and `stream_origin_region` / `stream_origin_cluster_id` (where the stream was ingested). Producer-stamped values always win; when a producer emits without them, Decklog backfills `source_region` / `source_cluster_id` from its own instance identity (`REGION` / `REGION_ID` / `DECKLOG_SOURCE_REGION` and `CLUSTER_ID` env). This guarantees MirrorMaker fan-in consumers always see a non-empty source — required for cross-region dedup and attribution ([multiregion-kafka-mirrormaker.md](multiregion-kafka-mirrormaker.md)). On the `MistTrigger` envelope, `cluster_id` / `origin_cluster_id` play the `source_cluster_id` / `stream_origin_cluster_id` roles. Region/cluster/tenant/event-type identity is mirrored into Kafka headers so consumers and the DLQ path can route without parsing bodies.

## Produced topics

| Topic                         | Env                          | Contents                                                                  |
| ----------------------------- | ---------------------------- | ------------------------------------------------------------------------- |
| `analytics_events`            | `ANALYTICS_KAFKA_TOPIC`      | Typed analytics envelope (JSON) from `SendEvent` / `SendGatewayTelemetry` |
| `service_events`              | `SERVICE_EVENTS_KAFKA_TOPIC` | Service-plane envelope (JSON) from `SendServiceEvent`                     |
| `analytics.raw_mist_triggers` | `DECKLOG_RAW_TRIGGERS_TOPIC` | Marshaled `MistTrigger` protobuf, final/accounting triggers only          |

Retention policy targets for these (and the DLQ below) are in [service-events.md](service-events.md) §7. `billing.usage_reports` is produced by Periscope Query, not Decklog ([meter-contracts.md](meter-contracts.md)).

## DLQ + retry contract (consumed downstream)

Decklog itself never consumes; the dead-letter topic `decklog_events_dlq` (`DECKLOG_DLQ_KAFKA_TOPIC`) is written by the **consumers** of Decklog's topics — Periscope Ingest and Signalman (DLQ encoding shared via `pkg/kafka/dlq.go`). The retry/backpressure contract below applies to **Periscope Ingest only** (`wrapWithDLQ` / `wrapRetryOnly` in `api_analytics_ingest/cmd/periscope/main.go`); Signalman has its own `wrapWithDLQ` (`api_realtime/cmd/signalman/main.go`) that does **no in-place retry** — any handler error dead-letters immediately and commits the offset. The Periscope Ingest contract:

- **Transient dependency failures retry in place.** Connection-class errors (ClickHouse/Kafka/network: refused, reset, bad conn, timeouts, EOF) are retried with exponential backoff (500 ms doubling, capped at 30 s) without committing the offset. Backpressure, not loss.
- **Non-retryable failures dead-letter, then commit.** Schema mismatches, constraint violations, and other permanent handler errors publish a `DLQPayload` to `decklog_events_dlq` and only then commit the offset. If the DLQ publish itself fails, the offset stays uncommitted — a message is never dropped between the source topic and the DLQ.
- **`wrapWithDLQ` vs `wrapRetryOnly`.** The `analytics_events` and `service_events` consumers (including their mirrored `{region}.`-prefixed variants) use `wrapWithDLQ`. The `analytics.raw_mist_triggers` consumer uses `wrapRetryOnly`: raw final-trigger projection is billing-critical, so a permanent failure blocks the partition for operator attention instead of committing past an accounting fact; poison protobuf payloads are logged and swallowed inside the handler itself.
- **Payload shape.** `DLQPayload` (JSON) carries the original topic/partition/offset/timestamp, base64-encoded key and value, the **original headers preserved verbatim**, the error string, and the consumer name. `tenant_id` / `event_id` / `event_type` are lifted into top-level fields (extracted from the message body when the headers lack them) so tenant-aware replay filters work without decoding payloads. DLQ records also re-emit `tenant_id` / `event_type` as Kafka headers plus `source` (consumer) and `original_topic`.
- **No dedicated replay service.** Replay is Kafka tooling: consume `decklog_events_dlq`, base64-decode the value, re-publish to `original_topic` with the preserved headers. Replays are safe where downstream dedup keys exist (deterministic `event_id`, `source_event_id`); keep the headers intact so enrichment and routing behave identically.

Failures are visible on `dlq_messages_total{topic, error_type}`.

## Key Files

- `api_firehose/internal/grpc/server.go` - all three RPCs, unwrap, event_id derivation, raw journal, envelope backfill
- `api_firehose/cmd/decklog/main.go` - topic/env wiring, TLS, Quartermaster bootstrap
- `pkg/clients/decklog/client.go` - producer-side client (respects pre-set `EventId`)
- `pkg/kafka/dlq.go` - `DLQPayload` + `EncodeDLQMessage`
- `api_analytics_ingest/cmd/periscope/main.go` - `wrapWithDLQ` / `wrapRetryOnly` consumer wrappers

## Related

- [analytics-pipeline.md](analytics-pipeline.md) - end-to-end analytics flow and the event-type → handler table
- [service-events.md](service-events.md) - service-plane taxonomy, topics, retention targets
- [trigger-durability.md](trigger-durability.md) - WAL/ack contract that makes ack-after-Kafka load-bearing
- [multiregion-kafka-mirrormaker.md](multiregion-kafka-mirrormaker.md) - why envelope backfill matters cross-region

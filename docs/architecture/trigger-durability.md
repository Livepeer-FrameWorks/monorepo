# Trigger durability

Final/accounting Mist triggers (USER_END, STREAM_END, PUSH_END, RECORDING_END, RECORDING_SEGMENT, LIVEPEER_SEGMENT_COMPLETE, PROCESS_AV_VIRTUAL_SEGMENT_COMPLETE) carry the ground-truth facts billing reads from. The pipeline must not drop them on the way from Mist to Kafka. This page describes the contract that guarantees that.

## Why it exists

Before this layer, `HandleUserEnd` in `api_sidecar/internal/handlers/handlers.go` responded `200 OK` to Mist immediately, then forwarded the parsed trigger to Foghorn (`api_balancing`) over the `HelmsmanControl` bidi stream with `stream.Send(...)`. A successful `Send` only means "queued on the bidi stream"; Helmsman had no way to know whether Foghorn received the message, whether Foghorn forwarded it to Decklog (`api_firehose`), or whether Decklog's Kafka publish committed. A network blip, Foghorn restart, or Kafka backpressure between accept and publish silently dropped the trigger. The accounting downstream then under-billed without surfacing anything.

The durability layer closes the gap with three changes:

1. Helmsman persists the trigger to a local write-ahead log before responding to Mist.
2. Foghorn emits a `MistTriggerAck` only after Decklog's `SendEvent` returns success (Decklog returns success only after its Kafka publish commits — so a positive ack means the trigger is durably ingested).
3. Helmsman waits for the positive ack before truncating the WAL row. On disconnect, restart, or negative-but-retryable ack, the entry stays on disk and replays.

The scope is intentionally narrow: only the seven final/accounting triggers above are wrapped. Best-effort triggers (heartbeats, `USER_NEW` admission, `STREAM_BUFFER` health, etc.) stay on the fire-and-forget path; their loss is recoverable from polling.

## Source of truth

- The WAL is **durable transport** — it guarantees no Mist trigger is dropped between Mist and the analytics path.
- The **billing source of truth** is the finalized-fact tables (`viewer_sessions_final`, `stream_sessions_final`, etc.) derived from the trigger payloads, not the WAL itself.
- Canonical 5-minute ledgers are deterministic projections of those finalized-fact tables; rollups are caches.

The WAL is therefore an operational safety net, not an authoritative store. Once the trigger has been durably published to Kafka, the WAL row's job is done.

## Source event id

Each trigger gets a stable id derived from the payload at Helmsman:

```
source_event_id = hex(sha256(node_id || 0x00 || trigger_type || 0x00 || payload_raw))
```

Implementation: `storage.ComputeSourceEventID` in `api_sidecar/internal/storage/trigger_wal.go`.

The id is stamped onto `MistTrigger.RequestId`; Foghorn uses it to address the ack back, and Decklog derives the typed Kafka `event_id` from it as a deterministic UUID. Periscope's current fact tables dedupe on UUID `event_id`, while `raw_mist_triggers` keeps the full source hash as `source_request_id`.

Identical re-deliveries from Mist hash to the same id and collapse on the WAL filename — the WAL is `append-only with idempotent natural key`, not a true journal of distinct attempts.

## WAL layout

`api_sidecar/internal/storage/trigger_wal.go`.

- Directory: `FRAMEWORKS_TRIGGER_WAL_DIR`, falling back to `$XDG_CACHE_HOME/frameworks/trigger-wal` or `/tmp/frameworks-trigger-wal`.
- One file per durable trigger: `<received_at_ms>-<source_event_id>.pb` containing the marshaled `pb.MistTrigger`.
- Writes are atomic: write to `.tmp`, `fsync`, `rename` into place, then fsync the WAL directory. Append returns only after the file and directory entry are durable.
- `Ack(source_event_id)` deletes the file (glob-on-id so any `received_at_ms` prefix works).
- `DeadLetter(source_event_id)` renames non-retryable rows to `.dead`; they are no longer retried but remain inspectable on disk.
- `Pending()` returns the protobuf-unmarshaled list in oldest-first order (sorted by filename, which has the millisecond prefix).
- 30-day TTL is implicit — the file stays until it is acked or manually purged. Operators should monitor pending depth.

The package is a Go-only library; tests in `trigger_wal_test.go` cover idempotent append, idempotent ack, crash-restart recovery (open a fresh handle on the same dir), and ordered drain.

## End-to-end flow

```
Mist
  POST /webhooks/mist/user_end (etc.)
    ↓
api_sidecar/internal/handlers (Helmsman)
  - read body
  - parse to *pb.MistTrigger, or wrap parse failures as RawMistWebhookTrigger
  - applyTenantContext()
  - forwardDurable() — stamps source_event_id on RequestId,
                       stamps deterministic UUID on EventId,
                       writes to WAL with fsync, kicks forwarder
  - respond 200 OK to Mist only after the durable write succeeds
    ↓ (returns to Mist)

(asynchronously)
api_sidecar/internal/control trigger_forwarder.go
  - drains WAL.Pending() in order
  - for each entry: stream.Send(ControlMessage_MistTrigger),
                    register ack channel keyed by source_event_id,
                    wait up to triggerAckTimeout (30s)
    ↓
api_balancing/internal/control/server.go (Foghorn)
  - processMistTrigger dispatches to MistTriggerProcessor
  - Processor enriches and forwards via Decklog client
  - Decklog.SendTrigger() → api_firehose SendEvent unary RPC
    → producer.PublishTypedEvent() + raw_mist_triggers publish → Kafka publish → ack
  - on Decklog return: sendMistTriggerAck(stream, requestID, err)
    ↓ (control stream)
api_sidecar handleMistTriggerAck
  - on success=true: WAL.Ack(source_event_id) → file deleted
  - on success=false, retryable=true: leave in WAL, next tick re-sends
  - on success=false, retryable=false: dead-letter the WAL file,
                                       log + metric; operator must inspect
  - on timeout: leave in WAL, next tick re-sends
```

## MistTriggerAck contract

Proto definition: `pkg/proto/ipc.proto` — `MistTriggerAck` + `TriggerAckErrorCode`.

- `request_id`: the stable `source_event_id` from the originating trigger. Required.
- `success`: true iff the trigger's typed analytics event and raw trigger journal event were durably published to Kafka through Decklog.
- `retryable`: only meaningful when `success=false`. True → Helmsman retries with the same `request_id`; downstream dedupes on the deterministic typed `event_id`. False → Helmsman dead-letters the entry for inspection; no automatic retry.
- `error_code`: enum mapping to the failure class. Transient codes (`INTERNAL`, `DOWNSTREAM_UNAVAILABLE`, `KAFKA_PUBLISH`) are retryable; permanent codes (`PARSE`, `SCHEMA`, `TENANT_MISSING`) are not.
- `error_message`: operator-facing detail, never customer-visible.

Foghorn maps processor errors via `classifyTriggerError` (`api_balancing/internal/control/server.go`).

## Failure modes and recovery

- **api_sidecar crashes between Mist's 200 OK and the next forwarder tick.** WAL is fsynced before the response, so the trigger survives. On restart, the forwarder calls `Pending()` and replays. Same `source_event_id` → idempotent across crashes.
- **api_balancing crashes during processing.** Helmsman's `awaitAck` times out after 30s, the next forwarder tick re-sends. Foghorn re-enriches and re-publishes; downstream dedup on `EventId`.
- **Decklog returns Kafka publish error.** Processor returns the error, Foghorn sends a negative retryable ack. WAL entry stays; next tick retries. This includes the raw trigger journal publish. If the underlying Kafka cluster is unavailable for hours, the WAL accumulates — operators see the pending-depth metric and can intervene.
- **WAL append fails.** Helmsman returns `503` for the trigger handler and emits `webhook_request_total{status="wal_error"}`. Final/accounting handlers must not acknowledge Mist as successfully accepted if the local durable write failed.
- **Helmsman parse/schema error after reading the body.** The raw body is wrapped in `RawMistWebhookTrigger` and durably journaled before `200 OK`, so MistServer parser drift cannot silently drop an accounting trigger. The raw envelope is operator-visible in `raw_mist_triggers`; typed final-fact projection simply skips it until the parser is fixed and the raw record is replayed.
- **Downstream non-retryable error (schema/tenant).** The WAL entry is moved to a `.dead` file for inspection and is not retried. Re-sending the same payload would fail the same way. Operator inspects by reading the WAL directory.
- **Trigger hash collision.** Mist would have to deliver two distinct triggers with identical `(node_id, trigger_type, payload_raw)`. SHA-256 collision in practice means this is impossible.

## Operational handles

- WAL directory pending file count is the canonical "is anything stuck?" signal.
- `webhook_request_total{trigger_type, status}` carries `durably_enqueued`, `durably_enqueued_parse_error`, and `wal_error` statuses for each final/accounting handler.
- `/internal/triggers/wal` lists pending rows and can kick an immediate drain. Replay is safe because retries use the same `source_event_id` and deterministic typed `event_id`; the WAL itself is idempotent by source id.

## Why not …

- **A new unary RPC.** The bidi stream is the existing control plane and already carries every other control message; adding a parallel transport would split the connection state.
- **Switching to Decklog directly from Helmsman.** Foghorn does tenant/cluster enrichment between Helmsman and Decklog (`ensureTriggerTenantID`, cluster_id population). Skipping it would force every edge node to know how to enrich, replicating Foghorn's identity work.
- **A SummingMergeTree journal of attempts.** The journal is a `ReplacingMergeTree` because we want exactly one logical row per `source_event_id`; an attempt log is interesting for incident review but not for billing. The forwarder already exposes retry counts via metrics.

## Related

- `pkg/proto/ipc.proto` — proto definitions
- `api_sidecar/internal/storage/trigger_wal.go` — WAL
- `api_sidecar/internal/control/trigger_forwarder.go` — forwarder
- `api_sidecar/internal/handlers/handlers.go` — Helmsman handlers (`forwardDurable`)
- `api_balancing/internal/control/server.go` — Foghorn ack emission (`sendMistTriggerAck`, `triggerTypesNeedingDurableAck`)
- `pkg/clients/decklog/client.go` — Decklog client; respects pre-set `EventId`
- `api_firehose/internal/grpc/server.go` — Decklog server; ack-after-Kafka
- `pkg/database/sql/clickhouse/periscope.sql` — `raw_mist_triggers` projection

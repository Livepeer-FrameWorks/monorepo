# Trigger durability

Final/accounting Mist triggers (USER_END, STREAM_END, PUSH_END, PUSH_INPUT_CLOSE, RECORDING_END, RECORDING_SEGMENT, LIVEPEER_SEGMENT_COMPLETE, PROCESS_AV_VIRTUAL_SEGMENT_COMPLETE) carry the ground-truth facts billing reads from. This page describes the durable delivery contract after Helmsman has accepted one of those HTTP posts. Mist's asynchronous trigger transport does not read the HTTP response, so the guarantee does not cover a connection failure or Helmsman failure before the local WAL append.

## Why it exists

Before this layer, `HandleUserEnd` in `api_sidecar/internal/handlers/handlers.go` responded `200 OK` to Mist immediately, then forwarded the parsed trigger to Foghorn (`api_balancing`) over the `HelmsmanControl` bidi stream with `stream.Send(...)`. A successful `Send` only means "queued on the bidi stream"; Helmsman had no way to know whether Foghorn received the message, whether Foghorn forwarded it to Decklog (`api_firehose`), or whether Decklog's Kafka publish committed. A network blip, Foghorn restart, or Kafka backpressure between accept and publish silently dropped the trigger. The accounting downstream then under-billed without surfacing anything.

The durability layer closes the gap with three changes:

1. Helmsman persists the trigger to a local write-ahead log before returning from the webhook handler.
2. Foghorn emits a `MistTriggerAck` only after Decklog's `SendEvent` returns success (Decklog returns success only after its Kafka publish commits — so a positive ack means the trigger is durably ingested).
3. Helmsman waits for the positive ack before truncating the WAL row. On disconnect, restart, or negative-but-retryable ack, the entry stays on disk and replays.

The scope is intentionally narrow: only the eight final/accounting triggers above are wrapped. Best-effort triggers (`STREAM_BUFFER`, `LIVE_TRACK_LIST`, `THUMBNAIL_UPDATED`, and `PROCESS_EXIT`) stay on the fire-and-forget path. Blocking policy triggers have their own synchronous outcome contract; see [Mist trigger contract](mist-trigger-contract.md).

## Separate media-control completion outbox

`<HELMSMAN_STATE_DIR>/control-outbox` is a second, distinct durability mechanism.
It protects replay-safe terminal Helmsman-to-Foghorn transitions: terminal
processing results (but not `cache_update` observations), `DVRStopped`,
`SyncComplete`, `ThumbnailUploaded`, `ArtifactDeleted`, DVR segment upload/drop
results, node-update apply results, and config-seed apply results. Processing/DVR
progress, cache-update observations, and storage lifecycle telemetry are live
samples and are dropped while disconnected; they must never evict terminal
transitions from a bounded retry queue.

Unlike the Mist-trigger WAL below, the media-control outbox generally has no
application-level acknowledgement. A successful send moves a row to an in-flight
state stamped with the current connection epoch; only a later heartbeat on that
same epoch confirms removal, while reconnect replays every older unconfirmed row.
Config-seed apply results add a second durable boundary: Foghorn does not accept
the stream message until it has stored the latest per-node result in its local
`config_seed_apply_ack_outbox`. Navigator delivery then retries from that table,
so a Navigator or Quartermaster outage does not block the media control stream or
lose an equal-version success/failure transition. The retained row's serialized
per-node revision is sent as `delivery_sequence`; Navigator persists it to reject
an older same-seed RPC that outlives its Foghorn lease. Equivalent applied/failed
bundle sets and the top-level success verdict deduplicate even when a Helmsman
restart supplies a fresh observation timestamp. Invalid persisted bytes are quarantined instead of retrying forever;
a same- or newer-seed ACK can replace the quarantined row. If the local write fails,
Foghorn terminates the control stream deliberately; Helmsman's durable control
outbox replays the unconfirmed result after reconnect. A trigger-WAL row instead remains until Foghorn explicitly
acknowledges the downstream Kafka commit. Do not describe the two mechanisms as
providing the same end-to-end guarantee.

Foghorn ordering values compared per node, stream, thumbnail asset, or artifact
copy use counters at that same key. During expand, new counters occupy the `2^52`
namespace and legacy global sequences stay low; moving legacy sequences into the
counter range would allow old and new binaries to issue conflicting authority.

`ArtifactDeleted` carries the original node deletion time in its payload. Durable
replay preserves that value; Foghorn applies the deletion only when the current
placement was not reported after it, so a reconnect cannot erase a copy the same
node has since reacquired. Every successful local file deletion first removes the
copy from Helmsman's inventory index. Foghorn also retains a per-copy deletion
watermark: an older inventory report that commits asynchronously after the
deletion cannot resurrect the placement, while a genuinely newer reacquisition
can. Placement writers preserve the greatest node observation time, so a delayed
sync completion cannot move that fence backward. Watermark rows are created only
by deletion and live until their artifact parent is removed; ordinary placement
refreshes do not create empty tombstones. Missing node timestamps from rolling-upgrade peers use the explicit
control-envelope `SentAt` when available, which is another node-clock reading.
Only a message lacking both timestamps uses zero, the permissive compatibility
value; Foghorn's unrelated wall clock is never substituted. The database
decision gates the corresponding local routing-state removal as well.

The control outbox intentionally has no automatic TTL or drop-oldest bound for
terminal transitions: exhausting local state is preferable to silently claiming
completion. Monitor `helmsman_control_outbox_pending` and
`helmsman_control_outbox_bytes`; investigate growth before the state volume fills.
Interrupted `.pb.tmp` writes and uncertain `.sent.<epoch>` rows are recovered at
local startup, without waiting for Foghorn, and checked again during reconnect. A
file-read error retains the committed row and stops that ordered drain initially;
after three consecutive read failures it is quarantined so one damaged filesystem
entry cannot block every later terminal transition. A row whose bytes cannot be
decoded as a control protobuf is counted separately and quarantined immediately.
If the quarantine rename itself fails, the drain stops and retains the row rather
than pretending that the head-of-line blocker was removed.
Quarantined rows are exposed through `helmsman_control_outbox_quarantined` and
`_quarantined_bytes`, and retained for 30 days for inspection before automatic
removal.

## Source of truth

- The WAL is **durable transport after local acceptance** — once `Append` succeeds, Helmsman retains the trigger until Foghorn acknowledges the committed Kafka publish or classifies it as permanently invalid.
- The **billing source of truth** is the finalized-fact tables (`viewer_sessions_final`, `stream_sessions_final`, etc.) derived from the trigger payloads, not the WAL itself.
- Canonical 5-minute ledgers are deterministic projections of those finalized-fact tables; rollups are caches.

The WAL is therefore an operational safety net, not an authoritative store. Once the trigger has been durably published to Kafka, the WAL row's job is done.

## Source event id

Each trigger gets a stable id derived from the payload at Helmsman:

```
source_event_id = hex(sha256(node_id || 0x00 || trigger_type || 0x00 || payload_raw [|| 0x00 || natural_key]))
```

Implementation: `storage.ComputeSourceEventID` in `api_sidecar/internal/storage/trigger_wal.go`.

The id is stamped onto `MistTrigger.RequestId`; Foghorn uses it to address the ack back, and Decklog derives the typed Kafka `event_id` from it as a deterministic UUID. Periscope's current fact tables dedupe on UUID `event_id`, while `raw_mist_triggers` keeps the full source hash as `source_request_id`.

The preferred natural key is Mist's retry-stable `X-Trigger-UUID` plus `X-Trigger-UnixMillis`, captured for both typed and parse-failure records. This collapses transport retries while keeping two distinct events with identical bodies separate. Legacy requests without those headers fall back to the body-derived key. The WAL is `append-only with idempotent natural key`, not a journal of delivery attempts.

For push-target status ordering, an absent `X-Trigger-UnixMillis` is also kept
as unknown. Known event times reject older observations and rank a terminal
status above `pushing` at an equal timestamp; unknown-time observations use
arrival order so a legacy `PUSH_OUT_START` can recover a target previously
reported failed.

## WAL layout

`api_sidecar/internal/storage/trigger_wal.go`.

- Directory: explicit `FRAMEWORKS_TRIGGER_WAL_DIR`, otherwise `<HELMSMAN_STATE_DIR>/trigger-wal`. Helmsman refuses startup without a durable state root; it never falls back to the reclaimable media volume, a user cache directory, or `/tmp`.
- One file per durable trigger: `<received_at_ms>-<source_event_id>.pb` containing the marshaled `pb.MistTrigger`.
- Writes are atomic: write to `.tmp`, `fsync`, `rename` into place, then fsync the WAL directory. Append returns only after the file and directory entry are durable.
- `Ack(source_event_id)` deletes the file (glob-on-id so any `received_at_ms` prefix works).
- `DeadLetter(source_event_id)` renames non-retryable rows to `.dead`; they are no longer retried but remain inspectable on disk.
- `Pending()` returns the protobuf-unmarshaled list in oldest-first order (sorted by filename, which has the millisecond prefix).
- No TTL — the file stays until it is acked or manually purged. Operators should monitor pending depth.

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
  - return 200 only after the durable write succeeds
    ↓ (Mist does not read this response for sync:false)

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
- `error_code`: enum mapping to the failure class. `retryable` is authoritative and independent of the enum: for example, `INTERNAL` may describe either a retryable infrastructure failure or a deterministic, permanent lifecycle outcome. `DOWNSTREAM_UNAVAILABLE` and `KAFKA_PUBLISH` are transient; `PARSE`, `SCHEMA`, and `TENANT_MISSING` are permanent.
- `error_message`: operator-facing detail, never customer-visible.

Foghorn maps processor errors via `classifyTriggerError` (`api_balancing/internal/control/server.go`).

## Failure modes and recovery

- **api_sidecar crashes between Mist's 200 OK and the next forwarder tick.** WAL is fsynced before the response, so the trigger survives. On restart, the forwarder calls `Pending()` and replays. Same `source_event_id` → idempotent across crashes.
- **api_balancing crashes during processing.** Helmsman's `awaitAck` times out after 30s, the next forwarder tick re-sends. Foghorn re-enriches and re-publishes; downstream dedup on `EventId`.
- **Decklog returns Kafka publish error.** Processor returns the error, Foghorn sends a negative retryable ack. WAL entry stays; next tick retries. This includes the raw trigger journal publish. If the underlying Kafka cluster is unavailable for hours, the WAL accumulates — operators see the pending-depth metric and can intervene.
- **WAL append fails.** Helmsman returns `503` for accurate diagnostics and emits `mist_webhook_requests_total{status="wal_error"}`. Mist does not read asynchronous trigger responses, so this status does not cause Mist to retry. The event was not accepted into the durable boundary.
- **Helmsman parse/schema error after reading the body.** The raw body is wrapped in `RawMistWebhookTrigger` and durably journaled before `200 OK`, so MistServer parser drift cannot silently drop an accounting trigger. The raw envelope is operator-visible in `raw_mist_triggers`; typed final-fact projection simply skips it until the parser is fixed and the raw record is replayed.
- **Downstream non-retryable error (schema/tenant).** The WAL entry is moved to a `.dead` file for inspection and is not retried. Re-sending the same payload would fail the same way. Operator inspects by reading the WAL directory.
- **Missing legacy trigger identity.** If Mist headers are absent, two distinct events with identical `(node_id, trigger_type, payload_raw)` collapse to the same source id. Current Mist supplies a distinct UUID for each logical trigger and reuses it only for that trigger's transport attempts, so the normal path does not have this ambiguity.

## Operational handles

- WAL directory pending file count is the canonical "is anything stuck?" signal.
- `mist_webhook_requests_total{trigger_type, status}` carries `durably_enqueued`, `durably_enqueued_parse_error`, and `wal_error` statuses for each final/accounting handler.
- `GET /triggers/wal` lists pending rows; `POST /triggers/wal/replay` kicks an immediate drain. Replay is safe because retries use the same `source_event_id` and deterministic typed `event_id`; the WAL itself is idempotent by source id.

## Why not …

- **A new unary RPC.** The bidi stream is the existing control plane and already carries every other control message; adding a parallel transport would split the connection state.
- **Switching to Decklog directly from Helmsman.** Foghorn does tenant/cluster enrichment between Helmsman and Decklog (`ensureTriggerTenantID`, cluster_id population). Skipping it would force every edge node to know how to enrich, replicating Foghorn's identity work.
- **A SummingMergeTree journal of attempts.** The journal is a `ReplacingMergeTree` because we want exactly one logical row per `source_event_id`; an attempt log is interesting for incident review but not for billing. The forwarder already exposes retry counts via metrics.

## Related

- `pkg/proto/ipc.proto` — proto definitions
- `api_sidecar/internal/storage/trigger_wal.go` — WAL
- `api_sidecar/internal/control/trigger_forwarder.go` — forwarder
- `api_sidecar/internal/handlers/handlers.go` — Helmsman handlers (`forwardDurable`)
- `api_balancing/internal/control/server.go` — Foghorn ack emission (`sendMistTriggerAck`; the durable-set gate is `mist.IsDurableTriggerType` in `pkg/mist/triggers.go`)
- `pkg/clients/decklog/client.go` — Decklog client; respects pre-set `EventId`
- `api_firehose/internal/grpc/server.go` — Decklog server; ack-after-Kafka
- `pkg/database/sql/clickhouse/periscope.sql` — `raw_mist_triggers` projection

## Known limitations (open — not yet ruled)

Behaviors reviewed and left in place pending an explicit decision; listed here
so audits stop rediscovering them.

- **The ConfigSeed ACK outbox retries without a give-up ceiling** (backoff
  capped at 5 minutes). Dropping an undelivered ACK would lose the edge's
  readiness state; alert on `foghorn_config_seed_apply_ack_outbox_oldest_
pending_seconds` instead.
- **Outbox rows are not purged on node disconnect and delivered rows are
  retained.** The pending row may hold the only demotion, and the retained
  row's `revision` is the per-node delivery fence. Row count is bounded by
  node count; a node-retirement protocol is the place to remove them.
- **An unconfigured Navigator accumulates pending ACK work** (bounded by node
  count). Treating "not configured" as delivered would lose the obligation.
- **ConfigSeed apply results are persisted synchronously on the control
  stream's Recv loop** (up to 5 s head-of-line per node; a persistence failure
  tears down the stream, which reconnects and replays). Durability must
  precede the stream's implicit acknowledgment.

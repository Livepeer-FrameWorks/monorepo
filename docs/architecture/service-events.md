# Service Events Backbone

## Purpose

The Service Events backbone provides a unified, typed telemetry stream across core services. It supports:

- **Audit trails** for lifecycle events (auth, streams, tenants, billing).
- **API usage metering** for billing and analytics.
- **Real-time messaging** routing to Signalman.
- **Operational visibility** through consistent event taxonomy.

This is **service-plane** telemetry; media-plane and Mist triggers remain on the analytics pipeline.

---

## 2. End‑to‑End Flow

```
Service Producer → Decklog (gRPC) → Kafka [service_events] → Periscope Ingest → ClickHouse
                                                                           ↓
                                           Periscope Metering → Kafka [billing.usage_reports] → Purser
                                                                           ↓
                                                                Usage records / billing
```

**Messaging path (real‑time)**:

```
Deckhand → Decklog → Kafka [service_events] → Signalman → GraphQL Subscriptions → UI
```

Each regional Signalman also consumes the `{source_region}.service_events` copies MirrorMaker2 writes into its Kafka cluster, so a subscriber attached to its own region's Bridge receives service events produced in every region. See [multiregion-kafka-mirrormaker.md](multiregion-kafka-mirrormaker.md).

**Note**: Signalman forwards messaging‑related service events (e.g., message/conversation updates) and Lookout's tenant-scoped `incident_updated` (to `CHANNEL_SYSTEM`, delivered only to that tenant). `api_request_batch` is ignored. A latent `skipper_investigation` → `CHANNEL_AI` routing branch exists in Signalman (`api_realtime/cmd/signalman/main.go`), but nothing currently produces that event type, so it is functionally unused today.

**Ownership path (Lookout)**:

```
Quartermaster outbox → Decklog (aggregator) → Kafka [service_events] → Lookout (group lookout-cluster-ownership)
```

Lookout reads only the aggregator's local `service_events`, because Quartermaster publishes there. It acts on `cluster_updated` alone: it re-reads the cluster's owner from Quartermaster and moves open incidents to it. See [lookout.md](lookout.md), "Ownership changes".

---

## 3. Transport & Envelope

**Protobuf source of truth**: `pkg/proto` (`ServiceEvent` + typed payloads).

**Kafka JSON envelope**: `pkg/kafka` (`ServiceEvent` struct).

Decklog converts protobuf `ServiceEvent` into JSON and publishes to `service_events`, keyed by event ID.

### 3.1 Domain events (`domain.events`)

Business facts that consumers act on (stream, media, billing, account, tenant, and cluster lifecycle) have their own contract and topic, separate from the telemetry and audit on `service_events`.

- **Registry.** Every event type is a protobuf message in `pkg/proto/events/public/v1` (the tenant contract) or `pkg/proto/events/internal/v1` (platform only; Go package `pkg/proto/events/internalv1`), annotated with `frameworks.events.event` (`pkg/proto/events/options.proto`): type string, visibility `PUBLIC` or `INTERNAL`, scope `TENANT` or `PLATFORM`, and aggregate. `pkg/events` builds the registry from those descriptors and refuses duplicate types, a visibility that does not match the package, or a missing scope. Public messages carry only identifiers and values a tenant already sees through the API: no internal stream names, node IDs, storage paths, URLs, IPs, or raw error text.
- **Scope.** A `TENANT` event carries its tenant UUID in the envelope; a `PLATFORM` event (cluster lifecycle) never does, and names an owner in its payload when it has one.
- **Producer outbox.** A producer builds the event with `events.New`, which assigns a UUIDv7, and inserts it with `outbox.Enqueue` in the transaction that commits the state change, into `<schema>.domain_event_outbox` (DDL in `pkg/events/outbox.TableDDL`). The shared `outbox.Relay` claims rows with a lease token and `SKIP LOCKED`, skips a row while an older row of the same aggregate is incomplete, sends the batch to Decklog, and records completion only while it still holds the lease. A retry after a lost acknowledgment sends the same event ID.
- **Decklog.** `PublishDomainEvents(DomainEventBatch)` validates the whole batch before producing anything: a missing or non-UUIDv7 ID, a tenant that does not match the scope, or a payload that does not decode as the registered message (including fields the message does not declare) is `INVALID_ARGUMENT`; an unregistered type is `FAILED_PRECONDITION`, so the producer keeps the row until Decklog is upgraded. Decklog never assigns an ID and returns only after Kafka acknowledged every record.
- **Record.** Key `<aggregate>/<aggregate_id>` (for example `streams/<uuid>`), so an aggregate's events stay on one of the topic's 12 partitions. The value is the binary protobuf payload. Headers follow CloudEvents 1.0 Kafka binary mode: `ce_specversion`, `ce_id`, `ce_type`, `ce_source`, `ce_time`, `ce_subject` (the key), `ce_dataschema` (full message name), `content-type: application/protobuf`, and the extensions `ce_visibility`, `ce_actorauthtype`, `ce_actoruserid`, `ce_actortokenhash`, `ce_aggregateversion`. `tenant_id`, `source_region`, and `source_cluster_id` keep the names every topic uses. Consumers decode with `events.ParseRecord`, which keeps fields added by a newer producer.
- **Actor.** `events.ActorFromContext` records the auth type, user ID, and, for API-token calls, the token record ID hashed with Bridge's usage hash (`events.HashIdentifier`, the function Bridge's usage tracker calls), so an event joins the API usage rows of the same token. A service called with another service's credentials takes the actor from the request's `common.RequestActor` instead (§4.2).
- **Compatibility.** `make proto-breaking` runs `buf breaking` against the latest release tag: public events at `FILE` level (field names are the webhook JSON), internal events and the envelope at `WIRE` level. Changes within `v1` are additive; a breaking change is a new message in a new major package.
- **Mirroring.** `domain.events` is in both MirrorMaker2 directions, like `service_events`: the aggregator's projections and webhook delivery read every region's events, and each regional Signalman delivers public events produced in any region.
- **Periscope Ingest.** Reads `domain.events` and every `<region>.domain.events` copy in its existing consumer group (`HandleDomainEventMessage`, `api_analytics_ingest/internal/handlers/domain_events.go`). Every public tenant event becomes an `api_events` row (event ID `ce_id`, actor in `user_id`, `actor_auth_type`, `actor_token_hash`; platform events have no row). An internal event gets a row only when it records a change to the tenant's own account, billing, cluster access, or webhook endpoints (`auditedInternalTypes`: `tenant.*`, `cluster.invite_*`, `cluster.subscription_*`, `billing.payment_created`, `billing.subscription_*`, `webhook.endpoint_auto_disabled`); `artifact.node_copy_changed` (it names a storage node) and `recording.chapter_ready` (a processing step) never reach the tenant audit table, and a new internal type stays out until it is listed. Clip, recording, and upload lifecycle events also become an `artifact_events` row (event ID `ce_id`, `record_source = 'domain_events'`) and an `artifact_state_current_v2` row. Copies of one event, whether local, mirrored, redelivered, or the legacy row a producer writes under the same ID, collapse on the event ID in `api_events_deduped` and `artifact_events_deduped`, and the choice between a legacy and a domain copy is fixed: `api_events_deduped` keeps the domain row (dotted type and actor), so every event a domain producer recorded reads with its domain type and only events without a domain counterpart keep a legacy name; `artifact_events_deduped` keeps Foghorn's lifecycle row, which carries the path, URL, expiry, progress, node, and speed fields the domain row lacks. A type the binary does not register is counted in `domain_events_total{status="unknown_type"}`, logged, and skipped; an undecodable record goes to the DLQ; a transient ClickHouse failure is retried in place.
- **Signalman.** Reads the same topics and delivers public events to their tenant's `CHANNEL_EVENTS` subscribers as `EVENT_TYPE_TENANT_EVENT` with a `TenantEvent` payload (`id`, `type`, `time`, `subject`, and the registered message in `google.protobuf.Any`). A record whose `ce_visibility` is not `public` is dropped before decoding, and one whose registered type is internal is dropped whatever its header says, so no internal event reaches a tenant. Delivery is once per `ce_id` within Signalman's event-ID window, which is separate from the legacy topics' window because dual-written legacy events share the ID. `CHANNEL_ALL` does not include `CHANNEL_EVENTS`.
- **Bosun** (`api_webhooks`, aggregator region only). Reads `domain.events` and every `<region>.domain.events` copy in the durable `bosun` group from the earliest offset. The same header and registry checks as Signalman drop internal, unknown, and tenantless records; an undecodable public record goes to the DLQ. One Postgres transaction stores the event (`bosun.webhook_events`, unique on `ce_id`, so local and mirrored copies collapse) and one delivery per subscribed enabled endpoint of its tenant created no later than the event; the handler returns, and the offset commits, only after that transaction committed, retrying a failed write in place. Bosun emits one internal type through its own `bosun.domain_event_outbox`: `webhook.endpoint_auto_disabled`, committed with the automatic disable of a failing endpoint.

---

## 4. Event Taxonomy

Event types are string constants emitted by services. The list below reflects current producers (not exhaustive).

### 4.1 API Usage (Bridge)

- `api_request_batch`: per flush window, one aggregate per tenant, auth type, operation type, operation name, and
  root-field signature (the sorted GraphQL root field names the requests resolved; operation names are chosen by the
  client). Bridge mints the event ID once, so a retried batch keeps it. Periscope writes `api_requests.root_fields`
  and `api_usage_5m.root_fields`; `api_usage_5m_v` keys on the signature, and a source event keeps the signature of
  its first stored copy.

### 4.2 Mutation Audit (owning services)

Bridge records no audit event of its own. Every mutation is audited by the service that owns the state, in the
transaction that commits it, with the caller as actor. When a service acts for a caller over its own service
credentials, it names the caller on the request as `common.RequestActor` (auth type, user ID, and the token hash it
computed with `events.ActorFromContext`, so the raw API token ID never leaves it), and the called service attributes
its event to that principal:

- Commodore → Foghorn: `CreateClip`, `CreateVodUpload`, `CompleteVodUpload`, `AbortVodUpload` (`clip.requested`,
  `upload.created`, `upload.completed`, `upload.aborted`), and `RemoteClipRequest` for a peer-created clip.
- Purser → Quartermaster: `BootstrapClusterAccess` and `MaterializeClusterAccess` from `CreateClusterSubscription`
  (`tenant.cluster_assigned`, `cluster.subscription_requested`). Purser's webhook, tier-reconciliation, and bootstrap
  grants name no caller and record Purser.

Transitions no caller requested carry no actor: Mist-driven `recording.*` and `clip.ready|failed`, and the
upload completion or abort that Foghorn's recovery jobs finish after the requesting call failed.

### 4.3 Service‑of‑Record + Lifecycle Events

**Auth (Commodore)**

- `auth_login_succeeded`, `auth_login_failed`, `auth_registered`, `auth_token_refreshed`
- `mist_admin_session_minted`
- `token_created`, `token_revoked`, `wallet_linked`, `wallet_unlinked`

**Tenant + Cluster (Quartermaster)**

- `tenant_created`, `tenant_updated`, `tenant_deleted`
- `tenant_cluster_assigned`, `tenant_cluster_unassigned`
- `cluster_created`, `cluster_updated` (platform-scoped with no `tenant_id` when the cluster has no owner: Decklog accepts them and Periscope-Ingest writes no tenant audit row)
- `cluster_invite_created`, `cluster_invite_revoked`
- `cluster_subscription_requested`, `cluster_subscription_approved`, `cluster_subscription_rejected`
- `node.fingerprint_unbound`: a platform operator deleted a node fingerprint binding (`UnbindNodeFingerprint`). Platform-scoped with no envelope `tenant_id`; the envelope `user_id` is the operator, and the `ClusterEvent` payload carries the node's cluster, the binding's tenant, the operator's reason, and `before_state` with only `node_id` and `fingerprint_id` (no fingerprint hashes, identity key, or IPs). No domain event.

**Streams (Commodore)**

- `stream_created`, `stream_updated`, `stream_deleted`
- `stream_key_created`, `stream_key_deleted`

**Artifacts (Commodore + Foghorn)**

- `artifact_registered` (Commodore)

Foghorn does **not** emit a derived `artifact_lifecycle` ServiceEvent. It emits the typed
clip/DVR/VOD lifecycle event (MistTrigger analytics); the analytics ingest service fans that out into
the `artifact_state_current` overlay and the `artifact_events` history.

**Billing (Purser)**

- `payment_created`, `payment_succeeded`, `payment_failed`
- `subscription_created`, `subscription_updated`, `subscription_canceled`
- `invoice_paid`, `invoice_payment_failed`
- `topup_created`, `topup_credited`, `topup_failed`

**Support (Deckhand)**

- `message_received`, `message_updated`, `conversation_created`, `conversation_updated`

**Incidents (Lookout)**

- `incident_updated` (tenant-scoped incidents only; platform incidents are not emitted). See [lookout.md](lookout.md).

**Marketing (Steward)**

- `marketing_contact_delivered`, `marketing_subscriber_created` (platform-scoped,
  payload-free operator activity facts)

---

## 5. Producers & Payloads

| Producer      | Payloads                                                                           | Source                                        |
| ------------- | ---------------------------------------------------------------------------------- | --------------------------------------------- |
| Bridge        | `APIRequestBatch`, `ArtifactEvent`, `BillingEvent`, `ClusterEvent`                 | GraphQL mutations + usage tracker             |
| Commodore     | `AuthEvent`, `StreamChangeEvent`, `StreamKeyEvent`, `TenantEvent`, `ArtifactEvent` | Auth + stream + retention + artifact registry |
| Quartermaster | `TenantEvent`, `ClusterEvent`                                                      | Tenant + cluster lifecycle                    |
| Purser        | `BillingEvent`                                                                     | Billing lifecycle (webhooks + gRPC)           |
| Deckhand      | `MessageLifecycleData`                                                             | Messaging lifecycle                           |
| Lookout       | `IncidentEvent`                                                                    | Tenant incident changes                       |
| Foghorn       | `ArtifactEvent`                                                                    | Artifact lifecycle (clip/DVR/VOD)             |
| Steward       | No typed payload                                                                   | Successful marketing form actions             |

**Notes**

- Commodore writes each service event into `commodore.service_event_outbox` inside the transaction that commits its state change (`enqueueEventTx`, `api_control/internal/grpc/service_event_outbox.go`); a failed insert fails the mutation. When the event has a domain counterpart (§3.1), the domain row goes into `commodore.domain_event_outbox` in the same transaction under the same event ID. Events that are the operation's only durable effect (a rejected sign-in, a Mist admin session mint) commit in their own transaction, and the operation fails when they cannot be recorded. `artifact_deleted` is Foghorn's: it commits with the deletion there.
- Commodore and Quartermaster store the event ID with every service event row (`event_id`), and the drain workers send it on every attempt, so a retry after a lost acknowledgment carries the same ID. Rows written before v0.3.11 have none and are sent under their row ID. Both workers settle rows only while they hold the claim's lease token.
- Quartermaster does not call Decklog inline: it enqueues its service events into its own transactional outbox (`quartermaster.service_event_outbox`, written inside the state-changing transaction, together with the domain event when the type has one) and a drain worker delivers them to Decklog with retry/backoff (`api_tenants/internal/grpc/service_event_outbox.go`). Events Deckhand forwards through Quartermaster's `EnqueueServiceEvent` RPC stay service events only and keep the ID Deckhand set. Quartermaster also keeps separate Navigator intent outboxes (`navigator_custom_domain_outbox`, `navigator_tenant_alias_outbox`) for custom-domain/tenant-alias intents — those carry DNS/ingress intents to Navigator, not service events, and the full pipeline is documented on the Quartermaster/Navigator side.
- Purser does not call Decklog inline either: each billing mutation writes its `BillingEvent` into `purser.billing_event_outbox` inside the mutation's own transaction (`EnqueueBillingEventTx` in `api_billing/internal/grpc`, `emitBillingEventTx`/`emitBillingEventsTx` in `api_billing/internal/handlers`), so a failed insert rolls the mutation back, and `runBillingOutboxWorker` delivers committed rows to Decklog. When the fact has a domain event (§3.1), it goes into `purser.domain_event_outbox` in the same transaction and the legacy row's `id` (the ID its dispatch sends) is the domain event's ID (`api_billing/internal/billingevents`). Domain events without a legacy counterpart: `billing.invoice_created` when an invoice is issued (period finalization or advance base fee; drafts and manual-review holds emit nothing), `billing.invoice_paid` when a card settlement covers an invoice or an invoice is issued with nothing to collect, `billing.topup_credited` for an x402 credit, `billing.details_updated` when `UpdateBillingDetails` changes a stored field, and `account.suspended` when the balance threshold suspends a tenant (committed with the suspension; Quartermaster, Commodore, and email calls run after the commit). `billing.payment_failed` is written only when a payment moves from pending to failed, not on a redelivered webhook. A failed charge on a Stripe-managed subscription (`invoice.payment_failed`) has no Purser payment or invoice: its `billing.payment_failed` leaves `payment_id` and `invoice_id` empty, names the Stripe invoice in `provider`/`provider_reference_id`, and carries Stripe's amount and currency, because Purser records no EUR amount for that charge. It commits with the `invoice_payment_failed` legacy row and the dunning increment in the transaction that also settles the Stripe event's `purser.webhook_events` row, so a redelivery or a claim taken over after the lease records nothing. The exception to transactional enqueue is x402 accounting-anomaly and RPC-error telemetry (`emitBillingTelemetryEvent`), written as a standalone insert whose failure is logged, because it reports an observed condition rather than a billing state change.
- Navigator has no service events. Its custom domain worker commits `custom_domain.verified` with the first `pending_verification → verified` transition and `custom_domain.failed` with the first entry into `cert_failed` since the domain's last issuance or reactivation (`failure_reported_at`), or with the move to `verification_failed` of a domain still pending 7 days after `verification_started_at` (`logic.CustomDomainVerificationPeriod`, a code constant). Re-verification and failed retries of a `cert_failed` domain emit nothing. The relay in `cmd/navigator` delivers `navigator.domain_event_outbox` to Decklog at `DECKLOG_GRPC_ADDR`.
- Foghorn's artifact events use the same transactional-outbox shape (`foghorn.artifact_event_outbox`); see the node-copy telemetry section of [analytics-pipeline.md](analytics-pipeline.md). Every transition with a domain type (§3.1) writes it into `foghorn.domain_event_outbox` in the same transaction, and its legacy row's `id` (the ID its dispatch sends) is the domain event's ID (`api_balancing/internal/artifactoutbox`, `EnqueueClipTransitionTx`/`EnqueueDVRTransitionTx`/`EnqueueVodTransitionTx`/`EnqueueArtifactNodeCopyTx`): `clip.requested` with the clip artifact insert, `clip.ready`/`upload.ready` with processing completion, `clip.failed`/`upload.failed` with a failed or exhausted processing job, `upload.created` with the upload artifact insert, `upload.completed` with `completing → processing`, `upload.failed` with a failed multipart completion, `upload.aborted` with `aborting → deleted`, `recording.started` with the first confirmed capture (`requested`/`starting → recording` in `UpdateDVRProgressByHash`), `recording.stopped` with the finalization claim of a recording whose capture was published (`EnqueueArtifactFactTx`; no legacy row, and a stale `finalizing` reclaim emits nothing), `recording.ready`/`recording.failed` with DVR finalization (`recording.ready` only for a recording with a chapter policy; a live-rewind-only recording finalizes without it), the internal `recording.chapter_ready` (keyed by the parent recording) with chapter finalization, and the internal `artifact.node_copy_changed` with every node-copy transition. A clip or DVR request refused before its artifact row exists emits no domain event; the caller receives the error. `Artifact.playback_id` stays empty: playback IDs are rotatable and Foghorn holds no current value at most emit sites, so receivers key on `artifact_id` and look the playback ID up. `artifact_deleted` (no domain type) is a Foghorn service event written in the transaction in which `DeleteClip`, `DeleteDVR`, or `DeleteVodAsset` marks a clip, recording, or upload deleted (or claims an in-flight upload's abort), once per deletion and by the cell that owns the artifact. It is attributed to the `requested_by_user_id` Commodore puts on the request, which a federated deletion forwards to the owning cell; Commodore's stream-cleanup and orphaned-clip deletions carry none.
- Foghorn's stream lifecycle comes from ingest sessions (`foghorn.ingest_sessions.stream_id`, `playable_at`): `stream.connected` commits with a new session generation in `MintIngestSession` (a re-fired trigger of the same generation emits nothing), `stream.live` with the compare-and-set of `playable_at` by the session's own node on its first playable `STREAM_BUFFER` (replicas and repeated buffers match nothing), and `stream.idle` with every transaction that ends a session: close, STREAM_END and runtime-absence reapers, PID-reuse supersession, projection failure, disconnect and never-projected reapers, and a refused placement claim. `STREAM_BUFFER` is best-effort, so a failed `playable_at` write loses that session's `stream.live`. Mist-native streams without ingest sessions, and sessions admitted without a public stream ID, emit none of the three. Their `playback_id` is not stored with the session: `stream.connected` takes it from the admission (`IngestSessionRequest.PlaybackID`), and `stream.live`/`stream.idle` read it from the shared stream registry by internal name before the transaction opens, so a registry that cannot resolve the stream yields an empty ID rather than a failed transition. Foghorn's database is per cell, so each cell's relay publishes only that cell's events.
- Media-authority refresh is a separate correctness pipeline, not a Decklog
  service event. Purser and Quartermaster mutations enqueue dedicated refresh
  outboxes transactionally; Commodore compiles signed replacements and keeps
  independent per-cell delivery obligations. Foghorn output/placement status
  likewise uses local durable outboxes so analytics/control recovery cannot
  change the media admission decision. See
  [Media-cluster authority](media-authority.md).
- Demo mode skips ServiceEvent emission in the Gateway.
- Only **metadata** is stored and broadcast for support events; message content is excluded.
- API usage aggregates include **HMAC-hashed** user/token identifiers for unique counts (no raw IDs stored).
- Commodore emits `media.retention_policy.changed`, `media.retention.override_applied`, and `media.retention.override_reset` for customer-tunable storage retention changes.
- Commodore emits `stream_updated` with `push_targets` or `playback_policy` in `changed_fields` when those stream-level controls change, and `playback_policy_changed` ArtifactEvents for clip/VOD playback policy changes. A push target status report from Foghorn emits `stream_updated` (`push_target_status`) and `multistream.status_changed` only when the stored status changes; a repeated revoke of an inactive API token emits nothing.
- Usage tracking reserves system tenant UUIDs (including an anonymous usage bucket) to avoid dropping unauthenticated traffic from rollups.
- Lookout selects a small source-checked allowlist of direct service events for
  Slack/Discord operator activity. It does not infer milestones or notify on
  every service event. Steward's two marketing events are allowed without a
  tenant because they contain no submitter data.

---

## 6. Storage & Rollups

**ClickHouse tables**

- `api_events` (audit log for the service_events and domain.events topics, sanitized for support events); read it through `api_events_deduped`, one row per event ID and tenant
- `api_requests` (raw usage batches from `api_request_batch`; `root_fields` holds the resolved GraphQL root fields; empty for batches from a Bridge that predates them and for MCP)
- `api_usage_5m` (canonical operational ledger)
- `api_usage_hourly` / `api_usage_daily` (dashboard rollups from `api_usage_5m`)

**Periscope Metering and Query**

- Periscope Metering reads `api_usage_5m_v` and emits `api_requests`,
  `api_errors`, `api_duration_ms`, and `api_complexity` as canonical usage
  records for Purser.
- Periscope Query reads dashboard rollups for longer API analytics views; it
  does not schedule or publish billing reports.
- Persists API breakdown detail in `usage_details`: `auth_type`, `operation_type`, `operation_name`, `unique_users`, and `unique_tokens`.

**Purser**

- Stores API usage as usage_records (`api_requests`, `api_errors`, `api_duration_ms`, `api_complexity`).
- Persists `api_breakdown` in `usage_details` for analytics/debug.

---

## 7. Kafka Topics & DLQ

**Topics and retention**

The canonical topics and their retention are declared once in `pkg/topology/kafka_topics.go` (`CanonicalTopics`):

| Topic                         | Retention | Notes                                                        |
| ----------------------------- | --------- | ------------------------------------------------------------ |
| `analytics_events`            | 7 days    | Short replay buffer; ClickHouse is the source of truth       |
| `service_events`              | 180 days  | Audit trail and API usage                                    |
| `domain.events`               | 180 days  | 12 partitions, replication factor 3, `cleanup.policy=delete` |
| `analytics.raw_mist_triggers` | 30 days   | Raw final-trigger journal                                    |
| `billing.usage_reports`       | 365 days  | Billing safety and dispute window                            |
| `decklog_events_dlq`          | 90 days   | Triage and replay window                                     |
| `lookout.incidents`           | 7 days    | Aggregator only, never mirrored                              |

The CLI refuses a manifest topic without `config.retention.ms`, and a canonical topic whose config, partition count (`domain.events`), or replication factor differs from `pkg/topology`. The Ansible Kafka role creates missing topics and applies the declared config to existing ones, describing each topic first and altering only differing keys. Local compose creates every topic with the same retention. `domain.events` has no env override; the other topic names still accept the per-service env overrides.

**DLQ**

- Periscope Ingest and Signalman wrap Kafka handlers and publish failures to `decklog_events_dlq`.
- Lookout's ownership consumer does not dead-letter: it skips malformed events and retries a `cluster_updated` it could not apply until it applies, without committing past it.
- DLQ payloads are JSON with base64-encoded keys/values and the original headers for replay.
- Include `tenant_id` and `event_type` headers on DLQ messages to keep tenant-aware replay filters and routing intact. A `domain.events` record has no `event_type` header, so the wrappers copy its `ce_type` into `event_type`.
- Wrapper semantics (retryable-vs-permanent classification, `wrapWithDLQ` vs `wrapRetryOnly`, payload encoding) are documented in [decklog.md](decklog.md).

**Replay**

- There is no dedicated replay service. Use Kafka tooling to consume from `decklog_events_dlq`, decode the payload, and re-publish to the original topic.
- Preserve headers (`tenant_id`, `event_type`, etc.) when replaying so downstream enrichment behaves consistently.

---

## 8. Exclusions

- No inter‑service RPC call tracking.
- No read‑only API requests beyond aggregate usage batches.
- No message content stored in analytics.

---

## 9. Source Files (Key)

- `pkg/proto`
- `api_firehose/internal/grpc`
- `api_gateway/internal/middleware`
- `api_gateway/internal/resolvers`
- `api_control/internal/grpc`
- `api_tenants/internal/grpc`
- `api_ticketing/internal/handlers`
- `api_realtime/cmd/signalman`
- `api_analytics_ingest/internal/handlers`
- `api_analytics_query/internal/handlers`
- `api_billing/internal/handlers`

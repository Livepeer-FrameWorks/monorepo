# Multistreaming Architecture

## Overview

Multistreaming pushes a live stream to external platforms (Twitch, YouTube, etc.) using MistServer's native RTMP/RTMPS/SRT push. Push targets are stored in Commodore and activated by Foghorn when a stream goes live on its origin node.

```
                          ┌──────────────┐
                          │  Twitch RTMP  │
                          └──────┬───────┘
User configures targets          │
via GraphQL → Commodore    ┌─────┴──────────┐
                           │   MistServer    │──→ YouTube RTMP
Stream goes live ──→       │  (origin node)  │──→ Facebook RTMP
PUSH_REWRITE fires ──→     │                 │──→ Kick RTMP
Foghorn opens signed       └─────┬──────────┘
cell-local targets ──→           │
Sends to Helmsman ──→      PUSH_OUT_START / PUSH_END
Helmsman calls                   │
PushStart() per target     Foghorn updates status
```

## Why Event-Driven (Not Auto-Push)

MistServer supports auto-push rules in its config, but we don't use them because:

- **Multi-tenancy** — Auto-push rules are global to a MistServer instance. We'd have to sync per-tenant push targets to every node, even nodes that never see that stream.
- **Origin-only** — Only the origin node (where the stream is ingested) should push. Edge nodes that pull the stream for viewer delivery should not push.
- **Lifecycle control** — We need push targets to activate on `PUSH_REWRITE` (stream validated and accepted) and deactivate on `STREAM_END`. Auto-push would start before validation.

## Data Flow

### Configuration (User → Commodore)

1. User creates a push target via GraphQL (`createPushTarget` mutation)
2. Gateway resolves to Commodore's `PushTargetService.CreatePushTarget` gRPC
3. Commodore stores in `commodore.push_targets` table with `tenant_id` isolation
4. Target URI is validated (`rtmp://`, `rtmps://`, `srt://` only) and encrypted before storage
5. Target is created with `is_enabled = true`, `status = 'idle'`

### Activation (Stream Goes Live)

1. Streamer starts broadcasting → MistServer fires `PUSH_REWRITE` trigger
2. Helmsman (sidecar on the node) forwards trigger to Foghorn
3. Foghorn verifies the publishing credential and tenant decision from signed local authority. In connected rollout mode it may shadow/claim through Commodore; an outage-owner admission uses only ready local authority.
4. Foghorn opens the exact version's cell-sealed push target payload and stamps it onto the durable ingest admission effects.
5. The admission-effects worker sends `ActivatePushTargets` to the origin Helmsman. It retains/rearms the same payload after Foghorn or Helmsman reconnect; a newer target version cannot be substituted beneath an existing publisher. Re-arms use exponential backoff, stop after 12 attempts in one unstable cycle, and reset that cycle only after the obligation remained stable for five minutes.
6. Foghorn reserves one slot from the tenant's existing `max_viewers` delivery-capacity pool per enabled destination. There is no separate restream quota.
7. Helmsman rejects private, loopback, link-local, metadata, or otherwise non-public destination addresses unless an operator explicitly allows the destination network.
8. Helmsman reconciles Mist's actual push list to the exact fenced target revision and starts missing targets.
9. MistServer begins pushing RTMP/SRT to each destination.

### Status Tracking

1. MistServer fires `PUSH_OUT_START` when a push connects
2. Helmsman maps the raw URI to its local fenced target identity and sends only a credential-free status report to Foghorn.
3. Foghorn matches the target against the admitted local payload and ignores stale generations/revisions.
4. Foghorn writes a push-status obligation to its local outbox. The worker updates Commodore after connectivity returns; status reporting cannot stop the push.

`PUSH_END` is durably journaled as a sanitized `RESTREAM_STATUS_FINAL` fact:

- If push ended cleanly → status = `"idle"`
- If push ended with error → status = `"failed"`, with a bounded sanitized error
- Missing byte observations are quarantined rather than estimated or billed
- A current enabled target that ends is re-armed as `"retrying"`

### Deactivation (Stream Ends)

1. MistServer fires `STREAM_END` trigger
2. Foghorn durably claims the offline effect and sends a generation-fenced `DeactivatePushTargets` message.
3. Helmsman stops tracked external pushes, confirms they are absent from `PushList`, and acknowledges convergence.
4. Foghorn completes the offline effect only after that acknowledgement; lost connections leave it retryable.

## Database Schema

```sql
-- commodore.push_targets
CREATE TABLE IF NOT EXISTS commodore.push_targets (
    id            UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id     UUID NOT NULL,
    stream_id     UUID NOT NULL REFERENCES commodore.streams(id) ON DELETE CASCADE,
    platform      VARCHAR(50),
    name          VARCHAR(255) NOT NULL,
    target_uri    TEXT NOT NULL, -- versioned encrypted application payload
    is_enabled    BOOLEAN DEFAULT TRUE,
    status        VARCHAR(50) DEFAULT 'idle',
    last_error    TEXT,
    last_pushed_at TIMESTAMP,
    created_at    TIMESTAMP DEFAULT NOW(),
    updated_at    TIMESTAMP DEFAULT NOW()
);

CREATE INDEX idx_push_targets_stream ON commodore.push_targets(tenant_id, stream_id);
```

## Proto Messages

### External API (Commodore ↔ Gateway)

- `PushTarget` — full target with masked URI for API responses
- `CreatePushTargetRequest` / `UpdatePushTargetRequest` / `DeletePushTargetRequest`
- `ListPushTargetsRequest` / `ListPushTargetsResponse`

### Internal (Commodore ↔ Foghorn)

- `PushTargetInternal` — target with unmasked URI for actual pushing
- `GetStreamPushTargetsRequest` / `GetStreamPushTargetsResponse`
- `UpdatePushTargetStatusRequest` — status feedback from Foghorn

Commodore stores the bounded machine `reason_code` separately from the
sanitized operator-facing `last_error`, so automation never has to parse prose.
The stable vocabulary is `unspecified`, `connected`, `completed`,
`destination_rejected`, `network_error`, `process_error`, `capacity_exhausted`,
`configuration_error`, `edge_upgrade_required`, and `stopped`.

### Control Channel (Foghorn ↔ Helmsman)

- `ControlMessage.ActivatePushTargets` — list of targets to push to
- `ControlMessage.DeactivatePushTargets` / `DeactivatePushTargetsResult` — stop and acknowledge an exact source generation
- `MistTrigger.restream_status` — credential-free lifecycle status and durable final fact

## URI Masking

Target URIs contain third-party stream keys and are conservatively masked in every user/API surface. Only the scheme, destination host, port, and a generic marker are returned:

```
rtmp://live.twitch.tv/app/live_abc123xyz
→ rtmp://live.twitch.tv/redacted
```

Service-authenticated admission RPCs (`GetStreamPushTargets`,
`ValidateStreamKey`) return unmasked URIs. The public `CheckStreamKey` RPC is
identity-only and does not load push targets or pull sources.
Autonomous cells receive target URIs only inside their X25519-sealed authority
section; historical recipient cells receive revocations without newly issued
secret material. See [Media-cluster authority](media-authority.md).

## Security

- **Tenant isolation**: All queries filter by `tenant_id`
- **URI validation**: Only `rtmp://`, `rtmps://`, `srt://` schemes accepted
- **Destination policy**: Public destinations are allowed by default. `RESTREAM_ALLOW_PRIVATE_DESTINATIONS=true` permits RFC-private destinations, while `RESTREAM_ALLOWED_PRIVATE_CIDRS` permits explicit CIDRs. `RESTREAM_DENIED_CIDRS` blocks operator-selected public or private ranges and takes precedence over either allow setting. Cloud metadata, unspecified, and multicast endpoints remain blocked unconditionally.
- **Resolution boundary**: Helmsman resolves and checks every hostname immediately before starting a push. A resolver outage is retryable: the unresolved target remains in desired state so an existing push is retained, while independently valid siblings can still converge. Because Mist performs its own final connection resolution, this check narrows DNS-rebinding exposure but cannot cryptographically pin the resolved address; operators should use egress firewall policy for a hard network boundary.
- **API masking**: Stream keys in target URIs are redacted in GraphQL responses
- **At-rest encryption**: Commodore uses the versioned `FIELD_ENCRYPTION_KEY` keyring (`FIELD_ENCRYPTION_PREVIOUS_KEYS` during field-key rotation) with a distinct push-target encryption purpose, and decrypts full URIs only for sealed internal activation. Runtime, bootstrap, and the migration retain the current JWT secret plus `FIELD_ENCRYPTION_LEGACY_SECRETS` as read-only v1/v2 inputs; this floorless JSON-array channel supports historical JWT secrets shorter than the current field-key minimum and never writes new ciphertext. While the 0.3.0 field migration is incomplete, rotate `JWT_SECRET` only after adding the exact outgoing JWT secret to `FIELD_ENCRYPTION_LEGACY_SECRETS`; before persisting the initial fence, the migration samples up to 32 legacy rows per column and proves the configured keyring can authenticate at least one. The persisted secret fingerprint is keyed by `FIELD_ENCRYPTION_KEY`. An undecryptable enabled target keeps its identity, marks the entire restream section incomplete, and gets an explicit configuration-failure status without denying publisher ingest or exposing a shorter desired set. Undecryptable rows are counted in `commodore.field_encryption_quarantine` and block verification. After repairing the key set, an operator sets `FIELD_ENCRYPTION_REQUEUE_QUARANTINE` to a new token for each retry run. `FIELD_ENCRYPTION_ALLOW_QUARANTINE=true` remains a data-loss acknowledgement, not a repair.
- **Permission check**: Mutations require `streams:write` and either ownership of the stream or a tenant owner/admin role

## Capacity and Metering

A restream destination is viewer-like delivery, not a human viewer. Each live target reserves one slot from the existing tenant `max_viewers` capacity. Its observed runtime contributes `delivered_minutes`, and bytes sent contribute `egress_gb`, both with `delivery_kind=restream` and a bounded `platform` dimension. Playback keeps its pre-v0.3.0 dimension-free billing identity (absence of `delivery_kind` means playback) so rolling replays collide with already processed usage and correction keys. Audience counts and `total_viewers` continue to use playback sessions only.

Periscope stores restream attempts in `restream_sessions_final`, quarantines incomplete facts in `restream_sessions_anomalous`, and projects playback plus restream into `delivery_usage_5m`. No `max_push_targets` or other restream-specific quota exists.

The v2 cursor migration seeds every established ledger from its v1 watermark before workers start. Only the new delivery ledger performs a retained 90-day bootstrap; the established viewer-audience and operational ledgers resume from their seed with the normal settlement lookback instead of replaying history. The sole delivery worker catches up in one-hour chunks, checkpoints every successful chunk, and drains bounded 1,000-row emission batches. A session-scoped PostgreSQL advisory lock is reacquired for every rebuild chunk and stale-close pass, bounding failover overlap to one idempotent chunk; connection or process loss releases ownership automatically. Every chunk has a ten-minute execution deadline, the leader gauge is one only while the lease is held, and cursor age remains observable between passes. Each v2 cursor is replacement-versioned by the processed watermark itself, so a delayed writer cannot overwrite a newer checkpoint. Tombstone lookups are bounded to the changed facts' source-time span, with a retained-horizon fallback for malformed zero timestamps, and use scalar identity plus window skip indexes. The delivery worker reads finalized playback and restream facts directly; the viewer worker never writes the delivery ledger. Every direct ledger-backed dashboard refresh discovers affected keys from tombstone-bearing raw tables, then computes values from the canonical deduplicated views. The contract refresh seeds upgrade-only discovery markers at the start of every invocation in spillable per-scope statements and records a receipt after each scope completes. It fails closed unless that invocation completes all six scopes and consumes both marker and receipt tables only after every dependent refresh succeeds, so a delayed contract run or an unledgered retry after a partial failure reseeds without operator-written rows or a postdeploy replay. This does not rewrite source ledgers or extend source TTLs. Tenant and per-stream daily analytics use the same session-end-day contract for playback audience and playback-plus-restream egress, refresh both the former and replacement day when a fact's terminal timestamp changes, and run hourly. Postdeploy refuses to expose delivery-backed rollups until the delivery cursor is within fifteen minutes of current settled facts; contract then seeds and refreshes retained daily analytics.

Non-terminal sanitized restream reports populate `restream_sessions_current`. If a node disappears and no fenced `RESTREAM_STATUS_FINAL` arrives within four hours, the elected stale-close worker appends a `restream_sessions_anomalous` row. That row is operational evidence only and never contributes rated delivery usage.

Every activation carries a runtime-attempt token that is rotated in the encrypted durable obligation when a terminal output is re-armed. Helmsman echoes that token on activation results and status reports. Finals from retired attempts still reach the immutable billing-fact path, but cannot release capacity, retire the replacement, or overwrite its operational status. A zero-ID URI-fallback final is likewise published and may update status, but never performs ambiguous runtime-identity side effects against a bound replacement.

Each restream projection-divergence occurrence is embedded as `_correction_occurrence` in the canonical natural-key JSON before it is stored. Both old and new billing readers therefore hash the same occurrence-bearing restream identity during a rolling deployment; the separate `occurrence_id` column remains diagnostic metadata rather than an additional hash suffix. Pre-v0.3.0 playback, processing, stream-runtime, and storage correction keys keep their original natural-key bytes and an empty occurrence field so replay still collides with historical adjustments.

## Existing MistServer Integration

| Function         | File       | Purpose                    |
| ---------------- | ---------- | -------------------------- |
| `PushStart()`    | `pkg/mist` | Start a push to target URI |
| `PushStop()`     | `pkg/mist` | Stop an active push        |
| `PushList()`     | `pkg/mist` | List active pushes         |
| `PUSH_OUT_START` | `pkg/mist` | Trigger when push connects |
| `PUSH_END`       | `pkg/mist` | Trigger when push stops    |

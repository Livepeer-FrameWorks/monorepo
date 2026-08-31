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
5. The admission-effects worker sends `ActivatePushTargets` to the origin Helmsman. It retains/rearms the same payload after Foghorn or Helmsman reconnect; a newer target version cannot be substituted beneath an existing publisher.
6. Helmsman calls `mist.PushStart(streamName, targetURI)` for each enabled target
7. MistServer begins pushing RTMP/SRT to each destination

### Status Tracking

1. MistServer fires `PUSH_OUT_START` when a push connects
2. Helmsman forwards to Foghorn
3. Foghorn matches the target against the admitted local payload.
4. Foghorn writes a push-status obligation to its local outbox. The worker updates Commodore after connectivity returns; status reporting cannot stop the push.

Same flow for `PUSH_END`:

- If push ended cleanly → status = `"idle"`
- If push ended with error → status = `"failed"`, `last_error` populated

### Deactivation (Stream Ends)

1. MistServer fires `STREAM_END` trigger
2. Foghorn sends `DeactivatePushTargets` control message to Helmsman
3. Helmsman calls `mist.PushStop()` for each tracked push
4. MistServer may also auto-stop pushes when the source stream ends

## Database Schema

```sql
-- commodore.push_targets
CREATE TABLE IF NOT EXISTS commodore.push_targets (
    id            UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id     UUID NOT NULL,
    stream_id     UUID NOT NULL REFERENCES commodore.streams(id) ON DELETE CASCADE,
    platform      VARCHAR(50),
    name          VARCHAR(255) NOT NULL,
    target_uri    VARCHAR(512) NOT NULL, -- encrypted application payload
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

### Control Channel (Foghorn ↔ Helmsman)

- `ControlMessage.ActivatePushTargets` — list of targets to push to
- `ControlMessage.DeactivatePushTargets` — stop all pushes for a stream
- `ControlMessage.PushTargetStatusUpdate` — Helmsman reports push status back

## URI Masking

Target URIs contain third-party stream keys and are masked in GraphQL responses. The masking logic in `Commodore.ListPushTargets` replaces the path component:

```
rtmp://live.twitch.tv/app/live_abc123xyz
→ rtmp://live.twitch.tv/app/live_****xxxx
```

Internal RPCs (`GetStreamPushTargets`, `ValidateStreamKey`) return unmasked URIs.
Autonomous cells receive target URIs only inside their X25519-sealed authority
section; historical recipient cells receive revocations without newly issued
secret material. See [Media-cluster authority](media-authority.md).

## Security

- **Tenant isolation**: All queries filter by `tenant_id`
- **URI validation**: Only `rtmp://`, `rtmps://`, `srt://` schemes accepted
- **API masking**: Stream keys in target URIs are redacted in GraphQL responses
- **At-rest encryption**: Commodore encrypts `target_uri` on create/update and decrypts it only for masked API responses or internal push RPCs
- **Permission check**: Mutations require `streams:write` permission

## Existing MistServer Integration

| Function         | File       | Purpose                    |
| ---------------- | ---------- | -------------------------- |
| `PushStart()`    | `pkg/mist` | Start a push to target URI |
| `PushStop()`     | `pkg/mist` | Stop an active push        |
| `PushList()`     | `pkg/mist` | List active pushes         |
| `PUSH_OUT_START` | `pkg/mist` | Trigger when push connects |
| `PUSH_END`       | `pkg/mist` | Trigger when push stops    |

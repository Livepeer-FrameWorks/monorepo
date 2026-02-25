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
Foghorn fetches targets    └─────┬──────────┘
from Commodore ──→               │
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
4. Target URI is validated (`rtmp://`, `rtmps://`, `srt://` only)
5. Target is created with `is_enabled = true`, `status = 'idle'`

### Activation (Stream Goes Live)

1. Streamer starts broadcasting → MistServer fires `PUSH_REWRITE` trigger
2. Helmsman (sidecar on the node) forwards trigger to Foghorn
3. Foghorn validates the stream key via `Commodore.ValidateStreamKey`
4. **Commodore returns push targets** in `ValidateStreamKeyResponse.push_targets` (same query that validates the key — no extra RPC)
5. Foghorn sends `ActivatePushTargets` control message to the origin Helmsman
6. Helmsman calls `mist.PushStart(streamName, targetURI)` for each enabled target
7. MistServer begins pushing RTMP/SRT to each destination

### Status Tracking

1. MistServer fires `PUSH_OUT_START` when a push connects
2. Helmsman forwards to Foghorn
3. Foghorn looks up the push target by stream name + target URI
4. Foghorn calls `Commodore.UpdatePushTargetStatus(id, "pushing")`

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
    target_uri    VARCHAR(512) NOT NULL,
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

## Security

- **Tenant isolation**: All queries filter by `tenant_id`
- **URI validation**: Only `rtmp://`, `rtmps://`, `srt://` schemes accepted
- **API masking**: Stream keys in target URIs are redacted in GraphQL responses
- **Permission check**: Mutations require `streams:write` permission
- **TODO**: Encrypt `target_uri` at rest (application-level AES-256-GCM)

## Existing MistServer Integration

| Function         | File                   | Purpose                    |
| ---------------- | ---------------------- | -------------------------- |
| `PushStart()`    | `pkg/mist/client.go`   | Start a push to target URI |
| `PushStop()`     | `pkg/mist/client.go`   | Stop an active push        |
| `PushList()`     | `pkg/mist/client.go`   | List active pushes         |
| `PUSH_OUT_START` | `pkg/mist/triggers.go` | Trigger when push connects |
| `PUSH_END`       | `pkg/mist/triggers.go` | Trigger when push stops    |

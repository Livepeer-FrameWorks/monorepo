# FrameWorks Heartbeat

Periodic check routine for agents managing live streaming infrastructure.
Load this file on demand — the summary in [skill.md](https://frameworks.network/SKILL.md) is sufficient for most sessions.

## Schedule

| Context                | Interval        |
| ---------------------- | --------------- |
| Active live streams    | Every 15–30 min |
| Idle (no live streams) | Every 2–4 hours |
| Skill version check    | Once per day    |

## Check Routine

### 1. Account Health

Read `account://status` (MCP) or query `me` (GraphQL).

- If `blockers` array is non-empty, resolve each:
  - `BILLING_DETAILS_MISSING` → call `update_billing_details`
  - `INSUFFICIENT_BALANCE` → call `topup_balance` or use x402 `submit_payment`
- If account is suspended, alert human immediately.

### 2. Balance

Read `billing://balance` (MCP) or query `balance` (GraphQL).

- Check `current_balance_cents` and `drain_rate_cents_per_hour`.
- Compute `estimated_hours_left = current_balance / drain_rate`.
- **Alert human** if balance < $5 with active streams.
- **Alert human** if `estimated_hours_left` < 2 hours.
- Consider auto-topup via `topup_balance` if configured.

### 3. Active Streams

Read `streams://list` (MCP) or query `streams` (GraphQL). For each stream where `status = "live"`:

1. Read `streams://{id}/health`.
2. If `health_status` is `degraded`:
   - Log it, but no immediate action needed.
3. If `health_status` is `critical`:
   - Run `diagnose_rebuffering` — check if origin ingest is dropping frames.
   - Run `diagnose_buffer_health` — check edge buffer saturation.
   - Run `diagnose_packet_loss` if rebuffering diagnosis suggests network issues.
   - **Alert human** with diagnosis summary and suggested action.
4. If stream has been live > 24 hours without activity, consider whether it's intentional.

### 4. Skill Updates

Fetch `skill.json` and compare `version` field against last known version.

- If version changed, re-read `SKILL.md` to pick up new capabilities or behavioral changes.
- This check is low-priority — once per day is sufficient.

## Output Rules

- **Nothing notable**: produce no output. Silent heartbeats are expected.
- **Action taken** (e.g., auto-topup): log a brief one-line summary.
- **Human attention needed**: surface the specific issue, what you've diagnosed, and recommended next step.

## Escalation Thresholds

| Condition                        | Action            |
| -------------------------------- | ----------------- |
| Balance < $5 with active streams | Alert human       |
| Estimated hours left < 2         | Alert human       |
| Stream health `critical`         | Diagnose + alert  |
| Account suspended                | Alert immediately |
| x402 payment failure             | Alert human       |
| Wallet signature rejected        | Alert human       |
| Balance < $20, no active streams | Log (no alert)    |
| Stream health `degraded`         | Log (no alert)    |

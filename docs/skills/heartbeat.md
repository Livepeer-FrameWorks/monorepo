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
  - `INSUFFICIENT_BALANCE` → call `topup_balance` or use x402 `submit_payment`
  - `PAYMENT_SETUP_REQUIRED` → a human completes payment setup on the dashboard's Billing page
  - `BILLING_STATUS_UNAVAILABLE` → retry later; nothing was charged
  - `ACCOUNT_SUSPENDED` → alert human immediately
- If a GraphQL WebSocket closes with code `4403` and reason `account deactivated`, stop
  reconnecting and alert human: the user was deactivated or the account closed.

### 2. Balance

Read `billing://balance` (MCP) or query `prepaidBalance` / `billingStatus` (GraphQL). Amounts are
in euro cents.

- Check `available_balance_cents` (settled minus reserved) and `drain_rate_cents_per_hour`.
  `estimated_hours_left` is included when a drain rate is known.
- At €0 available, running streams and their viewers continue, but new streams, new viewers and
  reconnects are refused. Below -€10 settled the account is suspended and running sessions stop.
- **Alert human** if `available_balance_cents` < 500 (€5) with active streams.
- **Alert human** if `estimated_hours_left` < 2 hours.
- Consider auto-topup via `topup_balance` if configured.

### 3. Active Streams

Read `streams://list` (MCP) or query `streams` (GraphQL). For each stream where `status = "live"`:

1. Read `streams://{id}/health` for `is_live`, current viewers and duration.
2. Run `get_stream_health_summary`. Its `status` is `healthy`, `warning` (issues in the time
   range) or `no_data`. On `warning`, log it; no immediate action needed.
3. If the summary keeps reporting issues, or viewers complain:
   - Run `diagnose_rebuffering` — check whether viewers stall.
   - Run `diagnose_buffer_health` — check edge buffer saturation.
   - Run `diagnose_packet_loss` if rebuffering diagnosis suggests network issues.
   - Each returns `status` `healthy`, `warning` or `critical`. On `critical`, **alert human**
     with the diagnosis summary and suggested action.
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
| Balance < €5 with active streams | Alert human       |
| Estimated hours left < 2         | Alert human       |
| Diagnosis `critical`             | Alert human       |
| Account suspended                | Alert immediately |
| Account deactivated (4403 close) | Alert immediately |
| Payment setup required           | Alert human       |
| x402 payment failure             | Alert human       |
| Wallet signature rejected        | Alert human       |
| Balance < €20, no active streams | Log (no alert)    |
| Health summary `warning`         | Log (no alert)    |

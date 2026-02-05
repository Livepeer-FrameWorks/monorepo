---
name: frameworks-network
version: 1.0.0
description: Multi-tenant live streaming. MCP-native with wallet auth and x402 payments.
homepage: https://frameworks.network
metadata:
  emoji: "📡"
  category: streaming
  api_base: https://api.frameworks.network
  graphql: https://api.frameworks.network/graphql
  mcp_discovery: https://api.frameworks.network/.well-known/mcp.json
---

# FrameWorks

Multi-tenant live streaming platform with three access layers and crypto-native auth.

## Skill Files

| File          | URL                                                 |
| ------------- | --------------------------------------------------- |
| skill.md      | https://frameworks.network/skill.md                 |
| skill.json    | https://frameworks.network/skill.json               |
| heartbeat.md  | https://frameworks.network/heartbeat.md             |
| MCP discovery | https://api.frameworks.network/.well-known/mcp.json |

## Platform Overview

### Interfaces

| Interface   | Endpoint        | Best For                                                                |
| ----------- | --------------- | ----------------------------------------------------------------------- |
| **MCP**     | `POST /mcp`     | Full agent integration — tools, resources, prompts. Richest experience. |
| **GraphQL** | `POST /graphql` | Typed queries/mutations/subscriptions. Good for custom integrations.    |
| **REST**    | `/auth/*`       | Authentication only (wallet login, JWT refresh).                        |

### Authentication Methods

| Method               | Headers                                                      | Use Case                                                           |
| -------------------- | ------------------------------------------------------------ | ------------------------------------------------------------------ |
| **Wallet** (EIP-191) | `X-Wallet-Address`, `X-Wallet-Signature`, `X-Wallet-Message` | Primary agent auth. Auto-provisions tenant on first login.         |
| **x402 Payment**     | `X-PAYMENT: <base64>`                                        | Gasless USDC payment per-request. Also acts as anti-abuse barrier. |
| **Bearer JWT**       | `Authorization: Bearer <token>`                              | Session token from wallet-login response.                          |

### What You Can Do

| Category        | MCP Tools                                 | MCP Resources                  | GraphQL                             |
| --------------- | ----------------------------------------- | ------------------------------ | ----------------------------------- |
| Streams         | create, update, delete, refresh keys      | list, details, health          | mutations + queries + subscriptions |
| Clips           | create from live/recorded, delete         | —                              | mutations + queries                 |
| DVR             | start/stop catch-up recording             | —                              | mutation                            |
| VOD             | upload, complete, abort, delete           | list, details                  | mutations + queries                 |
| Playback        | resolve viewer endpoints (geo-routed)     | —                              | query                               |
| Billing         | top up, submit payment, check deposits    | balance, pricing, transactions | queries                             |
| Analytics       | —                                         | usage, viewers, geographic     | queries                             |
| QoE Diagnostics | rebuffering, buffer, packet loss, routing | —                              | —                                   |
| Support         | search conversations                      | history                        | —                                   |
| API Exploration | introspect schema, generate queries       | schema catalog                 | introspection                       |

MCP: 27 tools, 18 resources, 8 prompts — full discovery via `tools/list` and `resources/list`.
GraphQL: introspection enabled at `/graphql` — full schema discovery built-in.

## Security Notes

- Never share private keys or seed phrases with third parties.
- Store agent credentials locally (see Credentials below).
- Only send authentication headers to `*.frameworks.network` domains.

## Credentials

Store credentials at `~/.config/frameworks/credentials.json`:

```json
{
  "wallet_address": "0x...",
  "jwt": "eyJ...",
  "api_base": "https://api.frameworks.network"
}
```

Or use environment variables: `FRAMEWORKS_WALLET_PRIVKEY`, `FRAMEWORKS_JWT`.

## Quick Start (Agent Flow)

1. **Create or load an EVM wallet.**
2. **Sign a wallet login message** (EIP-191) and call `POST /auth/wallet-login` to auto-provision a prepaid tenant.
3. **Fund the tenant** via x402 or crypto deposit.
4. **Connect** via MCP (`POST /mcp`) or GraphQL (`POST /graphql`) with wallet headers or JWT.
5. **Create a stream** and push RTMP using the stream key.

## Wallet Authentication

Headers:

- `X-Wallet-Address: 0x...`
- `X-Wallet-Signature: 0x...` (EIP-191 `personal_sign`)
- `X-Wallet-Message: <exact message>`

Message format (verbatim):

```
FrameWorks Login
Timestamp: 2025-01-15T12:00:00Z
Nonce: 12345
```

Wallet login endpoint: `POST /auth/wallet-login`

## MCP Configuration

Discovery: `GET /.well-known/mcp.json`
Endpoint: `POST /mcp`
Transport: HTTP + SSE (streamable-http)

### Example (Claude Desktop)

```json
{
  "mcpServers": {
    "frameworks": {
      "url": "https://api.frameworks.network/mcp",
      "headers": {
        "X-Wallet-Address": "0x...",
        "X-Wallet-Signature": "0x...",
        "X-Wallet-Message": "FrameWorks Login\nTimestamp: 2025-01-15T12:00:00Z\nNonce: 12345"
      }
    }
  }
}
```

## x402 Payments

Gasless USDC payments for instant top-ups or per-request auth. Also acts as an economic barrier against automated abuse.

- Header: `X-PAYMENT: <base64 payload>`
- Supported networks: Base, Arbitrum (USDC)

## GraphQL (Alternative Interface)

Endpoint: `POST /graphql`

Key operations:

- Mutations: `createStream`, `updateStream`, `deleteStream`, `refreshStreamKey`
- Queries: `streams`, `stream`, `me`, `balance`
- Subscriptions: `streamHealthUpdated`

Authentication: same wallet headers or bearer token.

## Rate Limits & Billing

- API requests are free; usage costs apply to viewer hours, storage, and processing.
- Prepaid balance must be positive to run billable operations.
- Use MCP `billing://balance` or GraphQL `balance` query to monitor balance and drain rate.

## Authentication Best Practices

- **Wallet-first.** Use EIP-191 wallet auth for initial login. It auto-provisions a tenant — no signup form needed.
- **Cache the JWT.** Wallet login returns a JWT. Reuse it for subsequent requests instead of re-signing every call.
- **Refresh before expiry.** JWTs expire. Use `POST /auth/refresh` proactively.
- **x402 for one-off payments.** If the human hasn't pre-funded the account, use x402 to pay per-request with USDC. Works on Base and Arbitrum.

## Streaming Best Practices

- **Check balance before creating streams.** Active streams drain balance continuously. Use `billing://balance` (MCP) or `balance` query (GraphQL) to check drain rate.
- **Monitor stream health.** Read `streams://{id}/health` periodically during live streams. Use QoE diagnostic tools if viewers report issues.
- **Top up proactively.** Streams are terminated if balance drops below -$10. Use x402 auto-payment or `topup_balance` to maintain buffer.
- **Clean up after yourself.** Delete streams, clips, and VOD assets you no longer need. Storage costs are ongoing.

## When to Alert Your Human

**Do alert:**

- Balance is critically low (< $5 with active streams)
- Stream health shows `critical` status
- Billing details are missing and you can't proceed
- x402 payment settlement fails
- Wallet signature is rejected (may need re-signing)

**Don't alert:**

- Routine balance checks
- Normal stream health readings
- Successful operations
- JWT refresh cycles

## Heartbeat (Periodic Check)

Run every 15–30 minutes during active streaming, every few hours otherwise.

1. **Account health**: Read `account://status`. Resolve any blockers.
2. **Balance**: Read `billing://balance`. Alert human if < $5 with active streams.
3. **Active streams**: Read `streams://list`. For each live stream, read `streams://{id}/health`. If `status: critical`, run `diagnose_rebuffering` and `diagnose_buffer_health`.
4. **Skill updates**: Check `skill.json` version periodically (once/day).

If nothing notable: no output needed.
If action required: surface the specific issue and recommended resolution.

For the full periodic check routine, load [heartbeat.md](https://frameworks.network/heartbeat.md).

## Preflight Errors

Billable MCP tools run preflight checks before execution. These are the blocking errors:

| Code                      | Trigger                           | Resolution                                                                                            |
| ------------------------- | --------------------------------- | ----------------------------------------------------------------------------------------------------- |
| `AUTHENTICATION_REQUIRED` | No wallet headers or bearer token | Send `X-Wallet-Address` + `X-Wallet-Signature` + `X-Wallet-Message`, or `Authorization: Bearer <jwt>` |
| `BILLING_DETAILS_MISSING` | Account has no billing address    | Call `update_billing_details` tool with address fields                                                |
| `INSUFFICIENT_BALANCE`    | Prepaid balance ≤ $0              | Pay via x402 (`submit_payment`) or `topup_balance`. Check `billing://balance` for current state       |

Rate limiting is handled at the Gateway layer (HTTP 429) with standard `Retry-After` headers — not as a preflight error.
Free operations (reads, listing, health checks) skip preflight entirely.

## Example: First Stream

### 1. Authenticate (REST)

- Sign an EIP-191 message: `"FrameWorks Login\nTimestamp: <ISO8601>\nNonce: <random>"`
- `POST /auth/wallet-login` with wallet headers → receive JWT + tenant auto-provisioned.

### 2. Connect (MCP or GraphQL)

- **MCP**: `POST /mcp` with `Authorization: Bearer <jwt>` or wallet headers.
- **GraphQL**: `POST /graphql` with same auth headers.

### 3. Resolve Blockers

- Read `account://status` (MCP) or query `me` (GraphQL) → check `blockers` array.
- If `BILLING_DETAILS_MISSING`: call `update_billing_details`.
- If `INSUFFICIENT_BALANCE`: use x402 (`X-PAYMENT` header with USDC on Base/Arbitrum) or `topup_balance`.

### 4. Create & Stream

- Call `create_stream` → capture `stream_key` + `rtmp_url`.
- Push RTMP: `rtmp://<ingest>/live/<stream_key>`.

### 5. Monitor

- Read `streams://{id}/health` periodically (or subscribe via GraphQL WebSocket).
- If issues: run QoE diagnostic tools (`diagnose_rebuffering`, `diagnose_buffer_health`).

### 6. Wrap Up

- `delete_stream` or leave for later.
- Check `billing://balance` to see what it cost.

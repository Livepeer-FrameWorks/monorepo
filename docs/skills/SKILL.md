---
name: frameworks-network
description: >
  Connect to FrameWorks live streaming platform via MCP. Create and manage
  live streams, VOD assets, clips, and DVR recordings. Monitor stream health
  with QoE diagnostics. Search streaming knowledge with RAG-grounded answers.
  Handle billing with wallet auth and x402 payments. Use when the user wants
  to stream video, manage live infrastructure, or integrate with FrameWorks.
compatibility: Requires network access to api.frameworks.network
metadata:
  author: frameworks
  version: "1.0"
  homepage: https://frameworks.network
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
| SKILL.md      | https://frameworks.network/SKILL.md                 |
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

| Category        | MCP Tools                                     | MCP Resources                  | GraphQL                             |
| --------------- | --------------------------------------------- | ------------------------------ | ----------------------------------- |
| Streams         | create, update, delete, refresh keys          | list, details, health          | mutations + queries + subscriptions |
| Clips           | create from live/recorded, delete             | —                              | mutations + queries                 |
| DVR             | start/stop catch-up recording                 | —                              | mutation                            |
| VOD             | upload, complete, abort, delete               | list, details                  | mutations + queries                 |
| Playback        | resolve viewer endpoints (geo-routed)         | —                              | query                               |
| Billing         | top up, submit payment, check deposits        | balance, pricing, transactions | queries                             |
| Analytics       | —                                             | usage, viewers, geographic     | queries                             |
| QoE Diagnostics | rebuffering, buffer, packet loss, routing     | —                              | —                                   |
| Support         | search conversations                          | history                        | —                                   |
| API Exploration | introspect schema, generate & execute queries | schema catalog                 | introspection                       |
| Knowledge       | ask_consultant                                | knowledge://sources            | —                                   |

MCP: 29 tools, 18 resources, 8 prompts — full discovery via `tools/list` and `resources/list`.
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

## Streaming Best Practices

- **Check balance before creating streams.** Active streams drain balance continuously. Use `billing://balance` (MCP) or `balance` query (GraphQL) to check drain rate.
- **Monitor stream health.** Read `streams://{id}/health` periodically during live streams. Use QoE diagnostic tools if viewers report issues.
- **Top up proactively.** Streams are terminated if balance drops below -$10. Use x402 auto-payment or `topup_balance` to maintain buffer.
- **Clean up after yourself.** Delete streams, clips, and VOD assets you no longer need. Storage costs are ongoing.

## Video Consultant (Skipper)

Use the `video_consultant` prompt for expert streaming guidance backed by a curated knowledge base.

- **Prompt**: `video_consultant` — activates expert streaming consultant mode
- **Tools**: `ask_consultant` — full Skipper pipeline with confidence tagging and source citations
- **Resource**: `knowledge://sources` — lists indexed documentation domains
- **Knowledge domains**: FrameWorks, MistServer, FFmpeg, OBS, SRT, HLS, nginx-rtmp, and ecosystem tools
- **Confidence tagging**: Every answer tagged as `verified`, `sourced`, `best_guess`, or `unknown` with citations

Use `ask_consultant` for full-quality answers with confidence tagging and citations.

## When to Alert Your Human

**Do alert:**

- Balance is critically low (< $5 with active streams)
- Stream health shows `critical` status
- Billing details are missing and you can't proceed
- x402 payment settlement fails
- Wallet signature is rejected (may need re-signing)

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

1. **Authenticate** — Sign EIP-191 message (`"FrameWorks Login\nTimestamp: <ISO8601>\nNonce: <random>"`), call `POST /auth/wallet-login` → receive JWT + tenant auto-provisioned.
2. **Connect** — `POST /mcp` or `POST /graphql` with `Authorization: Bearer <jwt>` or wallet headers.
3. **Resolve blockers** — Read `account://status` → check `blockers`. Fix `BILLING_DETAILS_MISSING` with `update_billing_details`, `INSUFFICIENT_BALANCE` with x402 or `topup_balance`.
4. **Create & stream** — `create_stream` → capture `stream_key` + `rtmp_url`. Push RTMP: `rtmp://<ingest>/live/<stream_key>`.
5. **Monitor** — Read `streams://{id}/health` periodically. If issues: `diagnose_rebuffering`, `diagnose_buffer_health`.
6. **Wrap up** — `delete_stream` or leave. Check `billing://balance` for cost.

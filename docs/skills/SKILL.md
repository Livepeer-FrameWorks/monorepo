---
name: frameworks-network
description: >
  Connect to FrameWorks live streaming platform via MCP. Create and manage
  live streams, VOD assets, clips, and DVR recordings. Monitor stream health
  with QoE diagnostics. Search streaming knowledge with RAG-grounded answers.
  Handle billing with wallet auth and x402 payments. Use when the user wants
  to stream video, manage live infrastructure, or integrate with FrameWorks.
compatibility: Requires network access to bridge.frameworks.network
metadata:
  author: frameworks
  version: "1.0"
  homepage: https://frameworks.network
  emoji: "📡"
  category: streaming
  api_base: https://bridge.frameworks.network
  graphql: https://bridge.frameworks.network/graphql
  mcp_discovery: https://bridge.frameworks.network/.well-known/mcp.json
---

# FrameWorks

Multi-tenant live streaming platform with three access layers and crypto-native auth.

## Skill Files

| File          | URL                                                    |
| ------------- | ------------------------------------------------------ |
| SKILL.md      | https://frameworks.network/SKILL.md                    |
| skill.json    | https://frameworks.network/skill.json                  |
| heartbeat.md  | https://frameworks.network/heartbeat.md                |
| MCP discovery | https://bridge.frameworks.network/.well-known/mcp.json |

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

MCP capabilities are registered by the Gateway at runtime. Use `tools/list`, `resources/list`, `resources/templates/list`, and `prompts/list` for the current inventory instead of relying on a hard-coded count.
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
  "api_base": "https://bridge.frameworks.network"
}
```

Or use environment variables: `FRAMEWORKS_WALLET_PRIVKEY`, `FRAMEWORKS_JWT`.

## Quick Start (Agent Flow)

1. **Call the MCP tool or GraphQL operation you need.**
2. **If the operation needs payment**, read the HTTP 402 / `INSUFFICIENT_BALANCE` response and its x402 payment requirements.
3. **Sign an EIP-3009 USDC authorization** for one accepted network.
4. **Retry the same operation** with `X-PAYMENT`.
5. **Use the returned stream key or resource.**

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

For browser or direct GraphQL integrations, use the `walletLogin` mutation to exchange the same address/message/signature fields for a JWT. The REST `POST /auth/wallet-login` endpoint is cookie-oriented for first-party sessions.

## MCP Configuration

Discovery: `GET /.well-known/mcp.json`
Endpoint: `POST /mcp`
Transport: HTTP + SSE (streamable-http)

### Example (Claude Desktop)

```json
{
  "mcpServers": {
    "frameworks": {
      "url": "https://bridge.frameworks.network/mcp",
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
- Queries: `streams`, `stream`, `me`, `billingStatus`, `prepaidBalance`
- Subscriptions: `liveStreamEvents`, `liveViewerMetrics`, `liveFirehose`

x402: make the GraphQL request, read the 402 payment requirements, then retry the same request with `X-PAYMENT`. Wallet and bearer auth are separate optional modes. Embedded playback resolution is public and uses the playback ID as the capability.

## Rate Limits & Billing

- API requests are free; usage costs apply to viewer hours, storage, and processing.
- Prepaid balance must be positive to run billable operations.
- Use MCP `billing://balance` or GraphQL `prepaidBalance` / `billingStatus` queries to monitor balance and drain rate.

## Streaming Best Practices

- **Check balance before creating streams.** Active streams drain balance continuously. Use `billing://balance` (MCP) or `prepaidBalance` / `billingStatus` queries (GraphQL) to check drain rate.
- **Monitor stream health.** Read `streams://{id}/health` periodically during live streams. Use QoE diagnostic tools if viewers report issues.
- **Top up proactively.** Streams are terminated if balance drops below -$10. Use x402 auto-payment or `topup_balance` to maintain buffer.
- **Clean up after yourself.** Delete streams, clips, and VOD assets you no longer need. Storage costs are ongoing.

## Video Consultant (Skipper)

Use `ask_consultant` to query the Skipper pipeline — knowledge retrieval, query rewriting, reranking, optional web search, and multi-step reasoning. Every answer includes confidence tagging and source citations.

### Knowledge Domains

| Domain     | Coverage                                                                 |
| ---------- | ------------------------------------------------------------------------ |
| FrameWorks | Platform docs: ingest, playback, API, cluster deployment, billing        |
| MistServer | Configuration, protocols, triggers, push targets, container formats      |
| FFmpeg     | Encoding: H.264, HEVC, VP9, AV1, hardware acceleration, bitrate control  |
| OBS        | Studio setup, streaming configuration, encoder settings, troubleshooting |
| SRT        | Protocol specification, configuration, latency tuning                    |
| HLS        | RFC 8216, playlist formats, segment encoding, LL-HLS                     |
| nginx-rtmp | Module configuration, directives, live streaming setup                   |
| Ecosystem  | Livepeer, WebRTC standards, DASH specification                           |

Read `knowledge://sources` for the live list of indexed URLs and sitemaps.

### Effective Queries

- **Be specific**: include protocol, codec, or tool name
  - Good: "How do I configure SRT latency in MistServer for a 500ms target?"
  - Weak: "How to reduce latency?"
- **Platform questions**: mention "FrameWorks" explicitly to prioritize platform docs
- **Mode**: `"docs"` for factual lookups (faster, no web), `"full"` (default) for web-augmented reasoning
- **Iterate**: if confidence is `best_guess` or `unknown`, rephrase with different terminology

### Confidence Tags

| Tag          | Meaning                           | Agent Action                      |
| ------------ | --------------------------------- | --------------------------------- |
| `verified`   | Grounded in indexed documentation | Safe for autonomous action        |
| `sourced`    | Found via web search with URL     | Act with verification             |
| `best_guess` | Inferred from adjacent knowledge  | Present to human for confirmation |
| `unknown`    | No strong evidence                | Do not act autonomously           |

### Tool Composition

For stream diagnostics, collect data first, then interpret:

1. `get_stream_health_summary` — overview (bitrate, FPS, issues)
2. Symptom-specific tool (`diagnose_rebuffering`, `diagnose_buffer_health`, `diagnose_packet_loss`, `diagnose_routing`)
3. `ask_consultant` — pass diagnostic JSON for expert recommendations

### Guided Workflows

| Prompt                                       | Use Case                            |
| -------------------------------------------- | ----------------------------------- |
| `video_consultant`                           | Expert streaming consultant persona |
| `diagnose_quality_issue(stream_id, symptom)` | Structured diagnostic workflow      |
| `agent_instructions`                         | Comprehensive MCP usage guide       |
| `troubleshoot_stream(stream_id)`             | Stream-specific issue resolution    |
| `optimize_costs`                             | Usage analysis and savings          |
| `api_integration_assistant(goal)`            | GraphQL API integration help        |

## Node Management

Agents that provision their own edge infrastructure can manage node lifecycle:

1. **Create cluster**: `create_private_cluster` — get bootstrap enrollment token
2. **Add nodes**: `create_enrollment_token` for additional edges in the same cluster
3. **Provision**: `frameworks edge provision --enrollment-token <token> --ssh user@host`
4. **Check health**: `get_node_info` for registration data, `get_node_health` for live metrics (CPU, RAM, bandwidth, active viewers)
5. **Set mode via API**: `set_node_mode` — no SSH needed, goes through Gateway → Commodore → Foghorn
6. **Set mode via CLI**: `frameworks edge mode draining` / `maintenance` / `normal` (local or `--ssh user@host`)
7. **Diagnose**: `frameworks edge doctor` + `frameworks edge logs`

Two management paths: use `set_node_mode` / `get_node_health` MCP tools when you don't have SSH access. Use CLI commands when you're on the edge or have SSH. Use `manage_node` for guided CLI command generation.

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
2. **Balance**: Read `billing://balance`. Alert human if `balance_cents` is below 500 with active streams.
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

1. **Call** — `POST /mcp` or `POST /graphql` with the desired billable operation.
2. **Pay if challenged** — On 402, sign one accepted x402 requirement and retry the same operation with `X-PAYMENT`.
3. **Resolve blockers** — If the response asks for billing details, call `update_billing_details`; if it asks for balance, retry with x402 or use `topup_balance`.
4. **Create & stream** — `create_stream` → capture `stream_key` + `rtmp_url`. Push RTMP: `rtmp://<ingest>/live/<stream_key>`.
5. **Monitor** — Read `streams://{id}/health` periodically. If issues: `diagnose_rebuffering`, `diagnose_buffer_health`.
6. **Wrap up** — `delete_stream` or leave. Check `billing://balance` for cost.

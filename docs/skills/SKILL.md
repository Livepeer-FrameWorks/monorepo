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

| Method               | Headers                                                      | Use Case                                                   |
| -------------------- | ------------------------------------------------------------ | ---------------------------------------------------------- |
| **Wallet** (EIP-191) | `X-Wallet-Address`, `X-Wallet-Signature`, `X-Wallet-Message` | Primary agent auth. Auto-provisions tenant on first login. |
| **x402 Payment**     | `PAYMENT-SIGNATURE: <base64>`                                | Authenticated, tenant-bound gasless USDC prepaid top-up.   |
| **Bearer JWT**       | `Authorization: Bearer <token>`                              | Session token from wallet-login response.                  |

### What You Can Do

| Category        | MCP Tools                                     | MCP Resources                                      | GraphQL                             |
| --------------- | --------------------------------------------- | -------------------------------------------------- | ----------------------------------- |
| Streams         | create, update, delete, refresh keys          | list, details, health                              | mutations + queries + subscriptions |
| Clips           | create from live/recorded, delete             | —                                                  | mutations + queries                 |
| DVR             | start/stop catch-up recording                 | —                                                  | mutation                            |
| VOD             | upload, complete, abort, delete               | list, details                                      | mutations + queries                 |
| Playback        | resolve viewer endpoints (geo-routed)         | —                                                  | query                               |
| Billing         | top up, submit payment, check deposits        | balance, pricing, transactions, invoices, payments | queries                             |
| Analytics       | —                                             | itemized usage, viewers, geographic                | queries                             |
| QoE Diagnostics | rebuffering, buffer, packet loss, routing     | —                                                  | —                                   |
| Support         | search conversations                          | history                                            | —                                   |
| API Exploration | introspect schema, generate & execute queries | schema catalog                                     | introspection                       |
| Knowledge       | ask_consultant                                | knowledge://sources                                | —                                   |

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

1. Request a single-use challenge from `POST /auth/wallet-challenge` or MCP `request_wallet_challenge`, sign it, and exchange it through wallet login or one MCP request.
2. Use the returned bearer session or create an API token.
3. If a rated operation needs payment, read its tenant-bound 402 requirements.
4. Sign an EIP-3009 USDC authorization and retry with `PAYMENT-SIGNATURE`.
5. Wait for confirmed credit before using the returned resource.

## Wallet Authentication

Headers:

- `X-Wallet-Address: 0x...`
- `X-Wallet-Signature: 0x...` (EIP-191 `personal_sign`)
- `X-Wallet-Message: <exact message>`

`X-Wallet-Message` must be the exact message returned by `POST
/auth/wallet-challenge` or MCP `request_wallet_challenge`; clients must not invent their own nonce. Challenges
expire after five minutes and are consumed atomically. The first MCP exchange
returns `X-Access-Token`; switch to that bearer token (or create an API token)
instead of replaying the wallet headers. REST `POST /auth/wallet-login` creates
the refreshable HttpOnly-cookie session used by the webapp.

After authentication, use `list_linked_wallets`, `link_wallet`, and
`unlink_wallet` to manage wallet identities. Linking requires a fresh challenge
for the new address. `link_email` adds a password sign-in and sends verification;
after verification, `activate_free_tier` enables metered, zero-priced Free access
without billing details or a payment provider. An account cannot unlink its final
wallet until a verified password sign-in is active.

## MCP Configuration

Discovery: `GET /.well-known/mcp.json`
Endpoint: `POST /mcp`
Transport: HTTP + SSE (streamable-http)

Configure long-lived MCP clients with `Authorization: Bearer <API token>`.
Wallet headers are deliberately single-use bootstrap credentials and are not
suitable for static client configuration.

API-token tool and resource discovery is scope-filtered, and resource reads
enforce the corresponding domain read scope. Grant only the domain permissions
the agent needs (`streams:*`, `billing:*`, `analytics:read`,
`infrastructure:*`, and the corresponding
account/support/developer/consultant/security scopes).
High-risk MCP tools additionally require `mcp:high-risk`; this is explicit
pre-authorization for unattended destructive, credential-changing, costly, or
arbitrary-query operations. Do not grant it to read-only agents.
Public operations need no credential, but a supplied API token remains bounded
by its scopes because authenticated responses may contain tenant-enriched data.

## x402 Payments

Official x402 v2 gasless USDC payments for confirmed prepaid top-ups.

- Header: `PAYMENT-SIGNATURE: <base64 payload>` (`X-PAYMENT` is legacy compatibility)
- Challenge: `PAYMENT-REQUIRED` (HTTP) or `payment_required` (MCP)
- Receipt: `PAYMENT-RESPONSE` (HTTP) or `payment_response` (MCP)
- Networks: use the live CAIP-2 options returned by the embedded facilitator; an optional hosted provider adds its `/supported` intersection
- Tax profile: payments at or below €100 can use the simplified-document path;
  over €100 or any VAT-number claim may require legal name, email, and address
- Pending: retry the identical payload when `SETTLEMENT_PENDING` returns a
  transaction hash/network; success alone returns `PAYMENT-RESPONSE`
- Side effects: top up through `submit_payment`, then retry without the payment
  header. Direct inline x402 mutation execution is rejected until the owning
  service registers durable idempotency.

## GraphQL (Alternative Interface)

Endpoint: `POST /graphql`

Key operations:

- Mutations: `createStream`, `updateStream`, `deleteStream`, `refreshStreamKey`
- Queries: `streams`, `stream`, `me`, `billingStatus`, `prepaidBalance`
- Subscriptions: `liveStreamEvents`, `liveViewerMetrics`, `liveFirehose`

x402: authenticate first, make the rated request, read the tenant-bound 402 requirements, then retry with `PAYMENT-SIGNATURE`. It tops up prepaid balance and never authenticates the caller. Embedded playback resolution is public and uses the playback ID as the capability.

## Rate Limits & Billing

- API and media usage is metered and itemized. While the billing-beta waiver is enabled, usage lines retain quantities and would-have-cost values but net to zero.
- Prepaid available balance (settled minus reserved) must be positive to start rated work. Reads, configuration, cleanup, developer credentials, and payment recovery remain available at zero balance.
- Use MCP `billing://balance` or GraphQL `prepaidBalance` / `billingStatus` queries to monitor balance and drain rate. Use `billing://documents` for retained invoices, receipts, and credit notes.

## Streaming Best Practices

- **Check balance before starting ingest or processing.** Stream configuration itself is available at zero balance; media admission is the rated boundary. Use `billing://balance` (MCP) or `prepaidBalance` / `billingStatus` queries (GraphQL).
- **Monitor stream health.** Read `streams://{id}/health` periodically during live streams. Use QoE diagnostic tools if viewers report issues.
- **Top up proactively.** Streams are terminated if balance drops below -€10. Use x402 auto-payment or `topup_balance` to maintain buffer.
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

1. **Create cluster**: `create_edge_cluster` — get bootstrap enrollment token and Foghorn address
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
- A payment specifically returns `BILLING_PROFILE_REQUIRED` and you cannot provide the listed fields
- x402 payment settlement fails
- Wallet signature is rejected (may need re-signing)

## Heartbeat (Periodic Check)

Run every 15–30 minutes during active streaming, every few hours otherwise.

1. **Account health**: Read `account://status`. Resolve any blockers.
2. **Balance**: Read `billing://balance`. Alert a human if
   `available_balance_cents` is below 500 with active streams; report settled
   and reserved balances as context.
3. **Active streams**: Read `streams://list`. For each live stream, read `streams://{id}/health`. If `status: critical`, run `diagnose_rebuffering` and `diagnose_buffer_health`.
4. **Skill updates**: Check `skill.json` version periodically (once/day).

If nothing notable: no output needed.
If action required: surface the specific issue and recommended resolution.

For the full periodic check routine, load [heartbeat.md](https://frameworks.network/heartbeat.md).

## Preflight Errors

MCP applies the shared operation class before execution. Rated tools can return these blockers; safe read/control/payment-recovery tools do not require balance:

| Code                       | Trigger                                                              | Resolution                                                                                                               |
| -------------------------- | -------------------------------------------------------------------- | ------------------------------------------------------------------------------------------------------------------------ |
| `AUTHENTICATION_REQUIRED`  | No wallet headers or bearer token                                    | Send `X-Wallet-Address` + `X-Wallet-Signature` + `X-Wallet-Message`, or `Authorization: Bearer <jwt>`                    |
| `BILLING_PROFILE_REQUIRED` | Payment is over €100 or carries a VAT claim and lacks a full profile | Call `update_billing_details` with the response's `required_fields`, then request fresh options                          |
| `INSUFFICIENT_BALANCE`     | Prepaid available balance ≤ $0                                       | Pay via x402 (`submit_payment`) or `topup_balance`. Check `billing://balance` for settled, reserved, and available state |

Rate limiting is handled at the Gateway layer (HTTP 429) with standard `Retry-After` headers — not as a preflight error.
Read, configuration, cleanup, billing, and payment-recovery operations skip the balance check. Authentication, permissions, and abuse limits still apply.

## Example: First Stream

1. **Call** — `POST /mcp` or `POST /graphql` with the desired billable operation.
2. **Pay if challenged** — On 402, sign one accepted v2 x402 requirement and submit it with `submit_payment`. If settlement is pending, retry that exact payment.
3. **Resolve blockers** — If the response asks for billing details, call `update_billing_details`; if it asks for balance, retry with x402 or use `topup_balance`.
4. **Retry rated work** — After confirmed credit, retry the rated operation without the payment header. Stream configuration itself may be created earlier at zero balance; ingest admission is the rated boundary.
5. **Monitor** — Read `streams://{id}/health` periodically. If issues: `diagnose_rebuffering`, `diagnose_buffer_health`.
6. **Wrap up** — `delete_stream` or leave. Check `billing://balance` for cost.

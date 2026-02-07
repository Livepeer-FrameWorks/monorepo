# Agent Access Architecture

Programmatic access for AI agents and autonomous clients: wallet auth, prepaid billing, x402 payments, MCP integration.

## Overview

1. **Wallet-based authentication** - Cryptographic identity via EVM wallet signatures
2. **Prepaid balance system** - Pay-as-you-go credits for wallet/agent accounts (postpaid exists for verified email)
3. **x402 protocol** - Gasless USDC payments for instant top-ups
4. **MCP adapter** - Model Context Protocol for AI-native tool discovery

## Agent Quick Start

1. **Create or load an EVM wallet.**
2. **Sign a wallet login message** and call `/auth/wallet-login` to auto-provision a prepaid tenant.
3. **Check `account://status`** to confirm readiness and blockers.
4. **Fund the tenant** via x402 (`X-PAYMENT`) or a crypto deposit.
5. **Create a stream** using MCP `create_stream`, then push RTMP with the returned stream key.

```
┌─────────────────────────────────────────────────────────────────┐
│                   AI Agent / Client / Claude Code                │
└─────────────────────────────────────────────────────────────────┘
                              │
                              ▼
┌─────────────────────────────────────────────────────────────────┐
│                   Gateway MCP (Hub)  bridge:18000/mcp           │
│                                                                 │
│  28 own tools + 1 proxied from Skipper spoke                    │
│  (ask_consultant)                                               │
└─────────────────────────────────────────────────────────────────┘
         │                    │                    │
         │        consumes ◄──┼──► provides        │
         │                    │                    │
         │   ┌────────────────┴──────────────┐     │
         │   ▼                               ▼     │
         │  ┌───────────────────────────────────┐  │
         │  │       Skipper (Spoke)              │  │
         │  │       skipper:18018                │  │
         │  │                                   │  │
         │  │  MCP Client ──► Gateway tools     │  │
         │  │  MCP Spoke  ──► ask_consultant     │  │
         │  │               (+ internal tools)   │  │
         │  │  Knowledge store (local pgvector)  │  │
         │  │  Heartbeat agent (direct gRPC)     │  │
         │  └───────────────────────────────────┘  │
         │                                         │
         ▼                    ▼                    ▼
┌─────────────────┐  ┌─────────────────┐  ┌─────────────────┐
│   Commodore     │  │     Purser      │  │   Periscope     │
│   (Auth/CRUD)   │  │   (Billing)     │  │   (Analytics)   │
│                 │  │                 │  │                 │
│ - Wallet→Tenant │  │ - Prepaid       │  │ - Usage by      │
│   mapping       │  │   balances      │  │   tenant/user   │
│ - Signature     │  │ - x402 settle   │  │ - API request   │
│   verification  │  │ - HD wallet     │  │   tracking      │
└─────────────────┘  └─────────────────┘  └─────────────────┘
```

---

## Discovery Endpoints

Public metadata served by the API gateway for agent and skill discovery. Source files in `docs/skills/`, routed by `api_gateway/cmd/bridge/main.go`.

| Path                                    | Standard     | Purpose                                               |
| --------------------------------------- | ------------ | ----------------------------------------------------- |
| `/.well-known/mcp.json`                 | MCP          | Server discovery (endpoint, transports, auth schemes) |
| `/.well-known/did.json`                 | W3C DID      | Decentralized identity; x402 verification + services  |
| `/.well-known/oauth-protected-resource` | RFC 8707     | OAuth resource metadata with wallet/x402 extensions   |
| `/.well-known/security.txt`             | RFC 9116     | Security contact and advisories                       |
| `/skill.json`                           | Agent Skills | Machine-readable skill metadata                       |
| `/SKILL.md`                             | Agent Skills | Human/LLM-readable quick-start guide                  |
| `/llms.txt`                             | Emerging     | LLM-friendly documentation index                      |
| `/robots.txt`                           | Standard     | Crawler directives (allows AI bots)                   |

These follow the [Agent Skills](https://agentskills.io) open standard adopted by Claude Code, OpenClaw, Cursor, Gemini CLI, and 25+ other agent products.

The DID document (`did.json`) substitutes `{{PLATFORM_X402_ADDRESS}}` at runtime from the environment.

---

## Wallet Authentication

EVM wallet identity system. Signature auth is currently Ethereum (EIP-191); Base/Arbitrum are used for x402 settlement.

### Headers

| Header               | Description                                 |
| -------------------- | ------------------------------------------- |
| `X-Wallet-Address`   | 0x-prefixed Ethereum address                |
| `X-Wallet-Signature` | EIP-191 `personal_sign` signature           |
| `X-Wallet-Message`   | Signed message (includes timestamp + nonce) |

### EIP-191 Message Format

Wallet login requires the following exact message format:

```
FrameWorks Login
Timestamp: 2025-01-15T12:00:00Z
Nonce: 12345
```

Notes:

- Timestamp must be ISO8601 UTC.
- Nonce can be any random string; it only needs to be unique per request.

### Signing Examples

TypeScript (viem):

```ts
import { createWalletClient, http } from "viem";
import { privateKeyToAccount } from "viem/accounts";

const account = privateKeyToAccount("0x...");
const client = createWalletClient({ account, transport: http() });

const message = [
  "FrameWorks Login",
  `Timestamp: ${new Date().toISOString()}`,
  `Nonce: ${crypto.randomUUID()}`,
].join("\n");

const signature = await client.signMessage({ message });
```

Python (eth-account):

```python
from eth_account import Account
from eth_account.messages import encode_defunct
import os
from datetime import datetime, timezone
import uuid

message = "\n".join([
    "FrameWorks Login",
    f"Timestamp: {datetime.now(timezone.utc).isoformat().replace('+00:00', 'Z')}",
    f"Nonce: {uuid.uuid4()}"
])

signed = Account.sign_message(
    encode_defunct(text=message),
    private_key=os.environ["FRAMEWORKS_WALLET_PRIVKEY"]
)
signature = signed.signature.hex()
```

**Notes**

- Header-based wallet auth is used for MCP/HTTP flows.
- GraphQL uses `walletLogin(input: WalletLoginInput!)` with the same address/message/signature fields.

### Auto-Provisioning

When a new wallet authenticates:

1. New tenant created with `billing_model = 'prepaid'` (mandatory)
2. New user created with `email = NULL`
3. Prepaid balance initialized at $0
4. Wallet identity record links wallet → user → tenant

### Trust Model

| Account Type            | Billing Model         | Trust Level                    |
| ----------------------- | --------------------- | ------------------------------ |
| Wallet-only             | `prepaid` (mandatory) | Low - must load balance first  |
| Email (verified)        | `postpaid` (invoiced) | High - use now, pay later      |
| Wallet + verified email | User choice           | High - can upgrade to postpaid |

### Key Files

- `pkg/database/sql/schema/commodore.sql` - `wallet_identities` table
- `api_control/internal/grpc/server.go` - `GetOrCreateWalletUser`, `WalletLogin`
- `pkg/auth/wallet.go` - EIP-191 signature verification + message validation

---

## Prepaid Balance System

Resource-based billing with prepaid credits. API requests are free; costs are for bandwidth/viewer hours, storage, and processing/transcoding.

Schema: `pkg/database/sql/schema/purser.sql` (`prepaid_balances`, `balance_transactions`)

### Enforcement

- Periscope usage summarizer runs every 5 minutes (cursor-based) and publishes usage summaries to Kafka
- Purser consumes usage summaries and deducts from `prepaid_balances`
- When balance < -$10: tenant subscription is suspended and active streams are terminated; new operations are blocked

### Top-Up Methods

1. **Card payments** - Stripe/Mollie checkout → credits balance
2. **Crypto deposits** - HD wallet address → block-explorer polling (Etherscan/Basescan/Arbiscan) → credits balance
3. **x402 payments** - Gasless USDC via EIP-3009 → instant credit

### Key Files

- `pkg/database/sql/schema/purser.sql` - Balance tables
- `api_billing/internal/handlers/jobs.go` - Billing enforcement
- `api_billing/internal/handlers/hdwallet.go` - HD wallet derivation
- `api_billing/internal/handlers/crypto.go` - Deposit monitoring

---

## x402 Protocol

Implementation of [x402](https://github.com/coinbase/x402) for gasless USDC payments using EIP-3009 "Transfer With Authorization".

### How It Works

1. Client makes request with insufficient balance
2. Server returns HTTP 402 with `PaymentRequirements` (payTo, asset, amount, network options)
3. Client signs EIP-3009 authorization off-chain
4. Client retries with `X-PAYMENT` header containing signed payload
5. Server verifies signature, submits tx on-chain (pays gas), credits balance

### Supported Networks

| Network  | ChainID | x402 | USDC Contract                                |
| -------- | ------- | ---- | -------------------------------------------- |
| Base     | 8453    | ✅   | `0x833589fCD6eDb6E08f4c7C32D4f71b54bdA02913` |
| Arbitrum | 42161   | ✅   | `0xaf88d065e77c8cC2239327C5EDb3A432268e5831` |
| Ethereum | 1       | ❌   | Too expensive (~$2-5/tx)                     |

**Note**: x402 uses a platform-wide `payTo` address (HD index 0). The payer identity comes from the signed authorization, not the address.

### 402 Response Format

```json
{
  "error": "insufficient_balance",
  "message": "Insufficient balance - please top up to continue",
  "code": "INSUFFICIENT_BALANCE",
  "operation": "resolveViewerEndpoint",
  "topup_url": "/account/billing",
  "x402Version": 1,
  "accepts": [
    {
      "scheme": "exact",
      "network": "base",
      "maxAmountRequired": "100000000",
      "payTo": "0x...",
      "asset": "0x833589fCD6eDb6E08f4c7C32D4f71b54bdA02913",
      "maxTimeoutSeconds": 60,
      "resource": "graphql://operation",
      "description": "Streaming, transcoding & storage credits via Base"
    }
  ]
}
```

### Token Limitation

x402 only works with EIP-3009 tokens (USDC). ETH/LPT use the deposit flow.

### Testnet Support (Local Development Only)

`X402_INCLUDE_TESTNETS=true` and `CRYPTO_INCLUDE_TESTNETS=true` add Base Sepolia and Arbitrum Sepolia to accepted networks. These flags exist for local development convenience only. There is no balance isolation — testnet payments credit real tenant balances identically to mainnet payments. Never enable in production.

### Gas Wallet

Single private key used on all EVM chains (same address everywhere):

- `X402_GAS_WALLET_PRIVKEY` (optional `X402_GAS_WALLET_ADDRESS` override)
- Fund with enough ETH on Base/Arbitrum for settlement gas
- Monitor via `gas_wallet_balance_eth` Prometheus metric

### Key Files

- `api_billing/internal/handlers/x402.go` - Verification + settlement
- `api_billing/internal/handlers/networks.go` - Network registry
- `api_billing/internal/handlers/gaswallet.go` - Balance monitoring
- `api_gateway/internal/middleware/ratelimit.go` - 402 response + X-PAYMENT handling

---

## MCP Adapter

Model Context Protocol integration for AI agent tool discovery, integrated into Gateway.

**Summary**: 29 tools (12 categories), 18 resources (9 categories), 8 prompts. The Gateway acts as a **hub** — it owns 28 tools directly and proxies 1 tool from the Skipper spoke.

| Category        | Tools                                                                              | Resources                                                            | Source        |
| --------------- | ---------------------------------------------------------------------------------- | -------------------------------------------------------------------- | ------------- |
| Account & Auth  | `update_billing_details`                                                           | `account://status`                                                   | Gateway       |
| Payment         | `get_payment_options`, `submit_payment`                                            | —                                                                    | Gateway       |
| Billing         | `topup_balance`, `check_topup`                                                     | `billing://balance`, `billing://pricing`, `billing://transactions`   | Gateway       |
| Streams         | `create_stream`, `update_stream`, `delete_stream`, `refresh_stream_key`            | `streams://list`, `streams://{id}`, `streams://{id}/health`          | Gateway       |
| Clips           | `create_clip`, `delete_clip`                                                       | —                                                                    | Gateway       |
| DVR             | `start_dvr`, `stop_dvr`                                                            | —                                                                    | Gateway       |
| VOD             | `create_vod_upload`, `complete_vod_upload`, `abort_vod_upload`, `delete_vod_asset` | `vod://list`, `vod://{artifact_hash}`                                | Gateway       |
| Playback        | `resolve_playback_endpoint`                                                        | —                                                                    | Gateway       |
| Analytics       | —                                                                                  | `analytics://usage`, `analytics://viewers`, `analytics://geographic` | Gateway       |
| QoE Diagnostics | 6 tools (`diagnose_*`, `get_stream_health_summary`, `get_anomaly_report`)          | —                                                                    | Gateway       |
| Support         | `search_support_history`                                                           | `support://conversations`, `support://conversations/{id}`            | Gateway       |
| Knowledge       | `ask_consultant`                                                                   | `knowledge://sources`                                                | Skipper spoke |
| Schema          | `introspect_schema`, `generate_query`, `execute_query`                             | `schema://catalog`                                                   | Gateway       |
| Infrastructure  | —                                                                                  | `nodes://list`, `nodes://{id}`                                       | Gateway       |

Code: `api_gateway/internal/mcp/` (tools, resources, prompts, preflight), `api_consultant/internal/` (mcpclient, mcpspoke, chat orchestrator). For full tool parameters, see the [public docs](https://docs.frameworks.network/agents/mcp/).

### Preflight Checks

Before billable operations, the preflight checker validates:

1. Authentication (tenant_id in context)
2. Billing details (required before billable operations)
3. Prepaid balance (positive balance required)

**Note**: x402 settlement enforces the €100 billing-details threshold for non-auth-only payments.

When balance is insufficient, the blocker response includes x402 payment options:

```go
type Blocker struct {
    Code        string        `json:"code"`
    Message     string        `json:"message"`
    Resolution  string        `json:"resolution"`
    Tool        string        `json:"tool,omitempty"`
    X402Accepts []X402Accept  `json:"x402_accepts,omitempty"`
}
```

### Hub-and-Spoke Architecture

The Gateway MCP acts as the **hub** — the single unified tool surface for external agents. Skipper (the AI Video Consultant) is a **spoke** that both consumes and provides tools through MCP.

**Gateway → Skipper (spoke)**: The Gateway proxies `ask_consultant` from Skipper's spoke endpoint (`/mcp/spoke`). The spoke authenticates via service token. The Gateway injects `tenant_id` from the caller's JWT context into forwarded arguments. The spoke also registers internal tools (`search_knowledge`, `search_web`) used by the orchestrator's pipeline but not exposed to external agents.

**Skipper → Gateway (client)**: Skipper's chat orchestrator consumes Gateway tools (QoE diagnostics, stream management, etc.) via an MCP client connection. Per-call JWT injection ensures each tool invocation carries the end user's auth context.

**Heartbeat agent**: Skipper's background heartbeat agent still uses direct gRPC clients (Periscope, Commodore, Purser, Quartermaster) for proactive health monitoring. These run as system-level operations without user JWT context.

Both directions degrade gracefully — if the other service is unavailable at startup, the dependent features log a warning and remain disabled.

---

## EU VAT Compliance

### Simplified Invoice Rule (x402)

- x402 top-ups generate **simplified invoices** in `purser.simplified_invoices`.
- Payments **≥€100** are blocked unless billing details are present.
- Full VAT invoice generation for x402 payments is **not** implemented.

### Location Evidence

Two pieces required for VAT rate determination:

1. IP geolocation
2. Wallet network (chain)

Schema: `pkg/database/sql/schema/purser.sql` (`simplified_invoices`)

Configuration: See `docker-compose.yml` and `api_billing/internal/config/config.go` for environment variables.

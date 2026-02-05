# Agent Access Architecture

This document describes the programmatic access system that enables AI agents and autonomous clients to interact with FrameWorks via wallet authentication, prepaid billing, x402 payments, and MCP integration.

## Overview

The agent access system provides:

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
│                        AI Agent / Client                        │
└─────────────────────────────────────────────────────────────────┘
                              │
                              ▼
┌─────────────────────────────────────────────────────────────────┐
│                      MCP Server Adapter                         │
│  (Integrated in Gateway - exposes tools/resources)              │
└─────────────────────────────────────────────────────────────────┘
                              │
                              ▼
┌─────────────────────────────────────────────────────────────────┐
│                      API Gateway (Bridge)                       │
│  ┌─────────────┐  ┌─────────────┐  ┌─────────────────────────┐  │
│  │ Wallet Auth │  │ JWT/Token   │  │ Prepaid Balance Check   │  │
│  │ Middleware  │  │ Middleware  │  │ + x402 Middleware       │  │
│  └─────────────┘  └─────────────┘  └─────────────────────────┘  │
└─────────────────────────────────────────────────────────────────┘
                              │
         ┌────────────────────┼────────────────────┐
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
| `/skill.md`                             | Agent Skills | Human/LLM-readable quick-start guide                  |
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

### Database Schema

```sql
-- Current balance per tenant
CREATE TABLE purser.prepaid_balances (
    tenant_id UUID NOT NULL,
    balance_cents BIGINT NOT NULL DEFAULT 0,
    currency VARCHAR(3) DEFAULT 'USD',
    low_balance_threshold_cents BIGINT DEFAULT 500,
    UNIQUE(tenant_id, currency)
);

-- Audit trail
CREATE TABLE purser.balance_transactions (
    tenant_id UUID NOT NULL,
    amount_cents BIGINT NOT NULL,        -- Positive = topup, negative = usage
    balance_after_cents BIGINT NOT NULL,
    transaction_type VARCHAR(20) NOT NULL, -- 'topup', 'usage', 'refund', 'adjustment'
    description TEXT,
    reference_id UUID
);
```

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

**Summary**: 27 tools (11 categories), 18 resources (9 categories), 8 prompts.

| Category        | Tools                                                                              | Resources                                                            |
| --------------- | ---------------------------------------------------------------------------------- | -------------------------------------------------------------------- |
| Account & Auth  | `update_billing_details`                                                           | `account://status`                                                   |
| Payment         | `get_payment_options`, `submit_payment`                                            | —                                                                    |
| Billing         | `topup_balance`, `check_topup`                                                     | `billing://balance`, `billing://pricing`, `billing://transactions`   |
| Streams         | `create_stream`, `update_stream`, `delete_stream`, `refresh_stream_key`            | `streams://list`, `streams://{id}`, `streams://{id}/health`          |
| Clips           | `create_clip`, `delete_clip`                                                       | —                                                                    |
| DVR             | `start_dvr`, `stop_dvr`                                                            | —                                                                    |
| VOD             | `create_vod_upload`, `complete_vod_upload`, `abort_vod_upload`, `delete_vod_asset` | `vod://list`, `vod://{artifact_hash}`                                |
| Playback        | `resolve_playback_endpoint`                                                        | —                                                                    |
| Analytics       | —                                                                                  | `analytics://usage`, `analytics://viewers`, `analytics://geographic` |
| QoE Diagnostics | 6 tools (`diagnose_*`, `get_stream_health_summary`, `get_anomaly_report`)          | —                                                                    |
| Support         | `search_support_history`                                                           | `support://conversations`, `support://conversations/{id}`            |
| Knowledge       | —                                                                                  | `knowledge://sources`                                                |
| Schema          | `introspect_schema`, `generate_query`                                              | `schema://catalog`                                                   |
| Infrastructure  | —                                                                                  | `nodes://list`, `nodes://{id}`                                       |

For full tool parameters, preflight error formats, and prompt details, see the [public docs](https://docs.frameworks.network/agents/mcp/).

### Structure

```
api_gateway/internal/mcp/
├── server.go           # MCP server setup
├── preflight/
│   └── checks.go       # Billing/balance checks with x402 integration
├── resources/
│   ├── account.go      # account://status
│   ├── analytics.go    # analytics://usage, analytics://viewers, analytics://geographic
│   ├── api_schema.go   # schema://catalog
│   ├── billing.go      # billing://balance, billing://pricing, billing://transactions
│   ├── knowledge.go    # knowledge://sources
│   ├── nodes.go        # nodes://list, nodes://{id}
│   ├── streams.go      # streams://list, streams://{id}
│   ├── support.go      # support://conversations, support://conversations/{id}
│   └── vod.go          # vod://list, vod://{artifact_hash}
├── tools/
│   ├── account.go      # update_billing_details
│   ├── api_assistant.go# introspect_schema, generate_query
│   ├── billing.go      # topup_balance, check_topup
│   ├── clips.go        # create_clip, delete_clip
│   ├── dvr.go          # start_dvr, stop_dvr
│   ├── payment.go      # get_payment_options, submit_payment
│   ├── playback.go     # resolve_playback_endpoint
│   ├── qoe.go          # diagnose_* + health/anomaly tools
│   ├── streams.go      # create_stream, update_stream, delete_stream, refresh_stream_key
│   ├── support.go      # search_support_history
│   └── vod.go          # create_vod_upload, complete_vod_upload, abort_vod_upload, delete_vod_asset
└── prompts/
    └── prompts.go      # Auth guidance
```

### Resources (Read-Only)

| URI Pattern                    | Description                                   |
| ------------------------------ | --------------------------------------------- |
| `account://status`             | Account readiness, blockers, and capabilities |
| `streams://list`               | List all streams                              |
| `streams://{id}`               | Stream details                                |
| `streams://{id}/health`        | Stream health metrics                         |
| `nodes://list`                 | Infrastructure nodes                          |
| `billing://balance`            | Prepaid balance                               |
| `billing://pricing`            | Current pricing rates                         |
| `billing://transactions`       | Balance transaction history                   |
| `analytics://usage`            | Usage aggregates                              |
| `analytics://viewers`          | Viewer metrics                                |
| `analytics://geographic`       | Geographic distribution                       |
| `support://conversations`      | Support conversation list                     |
| `support://conversations/{id}` | Support conversation detail                   |
| `knowledge://sources`          | Curated external documentation sources        |
| `schema://catalog`             | GraphQL schema catalog + templates            |
| `vod://list`                   | VOD assets                                    |
| `vod://{artifact_hash}`        | VOD asset details                             |

### Tools (Actions)

| Tool                        | Description                                   |
| --------------------------- | --------------------------------------------- |
| `update_billing_details`    | Set billing address and VAT details           |
| `topup_balance`             | Request crypto deposit address                |
| `check_topup`               | Check deposit status                          |
| `get_payment_options`       | Fetch x402 payment options (payTo + networks) |
| `submit_payment`            | Submit an x402 payment (auth-only or top-up)  |
| `create_stream`             | Create new live stream                        |
| `update_stream`             | Update stream settings                        |
| `delete_stream`             | Delete a stream                               |
| `refresh_stream_key`        | Rotate a stream key                           |
| `create_clip`               | Create clip from stream                       |
| `delete_clip`               | Delete clip                                   |
| `start_dvr`                 | Start DVR recording                           |
| `stop_dvr`                  | Stop DVR recording                            |
| `create_vod_upload`         | Begin VOD upload                              |
| `complete_vod_upload`       | Complete VOD upload                           |
| `abort_vod_upload`          | Abort VOD upload                              |
| `delete_vod_asset`          | Delete VOD asset                              |
| `resolve_playback_endpoint` | Resolve playback URLs for content             |
| `diagnose_rebuffering`      | QoE rebuffer analysis                         |
| `diagnose_buffer_health`    | QoE buffer state analysis                     |
| `diagnose_packet_loss`      | Packet loss analysis                          |
| `diagnose_routing`          | Routing decision analysis                     |
| `get_stream_health_summary` | Aggregated health metrics                     |
| `get_anomaly_report`        | Stream anomaly detection                      |
| `search_support_history`    | Search support conversations                  |
| `introspect_schema`         | Explore GraphQL schema                        |
| `generate_query`            | Generate GraphQL queries from templates       |

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

### MCP Consultant (Phase 1)

Phase 1 is implemented and focuses on:

- Curated knowledge sources (`knowledge://sources`).
- QoE diagnostics (`diagnose_*` tools) backed by Periscope.
- Support history access (`support://conversations`).

See: `api_gateway/internal/mcp/resources/*` and `api_gateway/internal/mcp/tools/*` for the authoritative list.

---

## Example: Agent Streaming Workflow

1. **Authenticate** with wallet headers and call `account://status`.
2. **Resolve blockers** (e.g., `update_billing_details`) if any are returned.
3. **Fund balance** using x402 `submit_payment` or `topup_balance`.
4. **Create stream** via `create_stream` and capture `stream_key`.
5. **Push RTMP** and monitor `streams://{id}/health`.
6. **Watch balance** via `billing://balance` to avoid unexpected shutdowns.

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

### Key Tables

```sql
CREATE TABLE purser.simplified_invoices (
    invoice_number VARCHAR(50) NOT NULL UNIQUE,
    tenant_id UUID NOT NULL,
    reference_type VARCHAR(20) NOT NULL,     -- x402_payment
    reference_id VARCHAR(255) NOT NULL,      -- tx_hash
    gross_amount_cents BIGINT NOT NULL,
    net_amount_cents BIGINT NOT NULL,
    vat_amount_cents BIGINT NOT NULL,
    vat_rate_bps INTEGER NOT NULL,
    currency VARCHAR(3) NOT NULL DEFAULT 'EUR',
    amount_eur_cents BIGINT NOT NULL,
    ecb_rate DECIMAL(10,6),
    evidence_ip_country VARCHAR(2),
    evidence_wallet_network VARCHAR(20),
    supplier_name VARCHAR(255) NOT NULL,
    supplier_address TEXT NOT NULL,
    supplier_vat_number VARCHAR(50) NOT NULL,
    issued_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
```

---

## Configuration

### Environment Variables

```bash
# Gas wallet (same key = same address on all chains)
X402_GAS_WALLET_PRIVKEY=0x...
X402_GAS_WALLET_ADDRESS=0x...   # Optional override
X402_INCLUDE_TESTNETS=true      # Dev only — no balance isolation, never use in production

# RPC endpoints
ETH_RPC_ENDPOINT=https://eth.publicnode.com
BASE_RPC_ENDPOINT=https://base.publicnode.com
ARBITRUM_RPC_ENDPOINT=https://arb1.arbitrum.io/rpc
BASE_SEPOLIA_RPC_ENDPOINT=https://base-sepolia.publicnode.com
ARBITRUM_SEPOLIA_RPC_ENDPOINT=https://sepolia-rollup.arbitrum.io/rpc

# Block explorer API keys (for deposit monitoring)
ETHERSCAN_API_KEY=...
BASESCAN_API_KEY=...
ARBISCAN_API_KEY=...
CRYPTO_INCLUDE_TESTNETS=true    # Dev only — no balance isolation, never use in production

# HD Wallet (for deposit addresses)
HD_WALLET_XPUB=xpub...

# Supplier info for invoicing (REQUIRED)
SUPPLIER_NAME=Your Company B.V.
SUPPLIER_ADDRESS=City, Country
SUPPLIER_VAT_NUMBER=XX123456789

# GeoIP database (for VAT country detection)
GEOIP_MMDB_PATH=/path/to/GeoLite2-City.mmdb
```

### VAT Handling

VAT rates are determined using a hybrid approach:

1. **Billing country** - If tenant has billing address, use that country
2. **GeoIP** - Fall back to client IP geolocation
3. **B2B exemption** - If tenant has valid EU VAT number, apply 0% (reverse charge)
4. **Non-EU** - Export exempt (0% VAT)

EUR conversion uses ECB daily rates (24h cache via frankfurter.app API).

---

## Key Design Decisions

| Decision             | Choice                | Rationale                                          |
| -------------------- | --------------------- | -------------------------------------------------- |
| Wallet billing model | Mandatory prepaid     | Sybil resistance via economic barriers             |
| MCP hosting          | Integrated in Gateway | Shares auth context, simpler than separate service |
| x402 facilitator     | Self-hosted in Purser | No CDP dependency; L2 gas costs vary               |
| x402 tokens          | USDC only             | EIP-3009 required (ETH/LPT use deposit flow)       |
| x402 networks        | Base + Arbitrum       | Both L2s have cheap gas                            |
| Balance expiry       | Never                 | Prepaid credits don't expire                       |
| API request billing  | Free                  | Costs are resource-based (viewer hours, etc.)      |
| Minimum top-up       | None                  | Accept any positive amount                         |

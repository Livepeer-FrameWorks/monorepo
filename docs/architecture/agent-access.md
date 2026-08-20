# Agent Access Architecture

Programmatic access for AI agents and autonomous clients: wallet auth, prepaid billing, x402 payments, MCP integration.

## Overview

1. **Wallet-based authentication** - Cryptographic identity via EVM wallet signatures
2. **Prepaid balance system** - Pay-as-you-go credits for wallet/agent accounts (postpaid exists for verified email)
3. **x402-compatible payments** - Gasless USDC prepaid top-ups
4. **MCP adapter** - Model Context Protocol for AI-native tool discovery

## Agent Quick Start

1. **Create or load an EVM wallet.**
2. **Call the MCP tool or GraphQL operation.**
3. **If payment is required**, the Gateway returns HTTP 402 / `INSUFFICIENT_BALANCE` with x402 requirements.
4. **Retry the same operation** with `PAYMENT-SIGNATURE` (legacy `X-PAYMENT` is also accepted).
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
│  Runtime-discovered Gateway tools + proxied ask_consultant       │
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

Public metadata served by the API gateway for agent and skill discovery. Source files in `docs/skills/`, routed by `api_gateway/cmd/bridge`.

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

The DID document (`did.json`) substitutes `{{X402_GAS_WALLET_ADDRESS}}` at runtime from the environment.

---

## Wallet Authentication

EVM wallet identity system. Signature auth is currently Ethereum (EIP-191); Base/Arbitrum are used for x402 settlement.

### Headers

| Header               | Description                               |
| -------------------- | ----------------------------------------- |
| `X-Wallet-Address`   | 0x-prefixed Ethereum address              |
| `X-Wallet-Signature` | EIP-191 `personal_sign` signature         |
| `X-Wallet-Message`   | Exact server-issued, single-use challenge |

### Challenge issuance and exchange

Clients first call `POST /auth/wallet-challenge` or the public MCP
`request_wallet_challenge` tool with the wallet address and chain ID. Commodore
normalizes the address, creates an EIP-4361-style message, stores only its
SHA-256 hash, and expires it after five minutes. A response is similar to:

```
app.frameworks.network wants you to sign in with your Ethereum account:
0x...

Sign in to FrameWorks.

URI: https://app.frameworks.network
Version: 1
Chain ID: 1
Nonce: <server nonce>
Issued At: 2026-08-20T00:00:00Z
Expiration Time: 2026-08-20T00:05:00Z
```

- The client must sign the returned bytes verbatim; it must not construct its
  own timestamp or nonce.
- `WalletLogin` verifies the EIP-191 signature and atomically consumes the
  matching unexpired challenge. Concurrent exchange or replay has one winner.
- REST login returns a refreshable HttpOnly-cookie session. MCP header exchange
  returns `X-Access-Token`; the client then switches to bearer/API-token auth.
- x402 is not accepted as authentication and cannot issue a session.
- REST and MCP wallet bootstrap calls share the public per-IP abuse bucket.
  Authentication throttling returns 429 and never produces an x402 challenge.

### Signing Examples

TypeScript (viem):

```ts
import { createWalletClient, http } from "viem";
import { privateKeyToAccount } from "viem/accounts";

const account = privateKeyToAccount("0x...");
const client = createWalletClient({ account, transport: http() });

const challenge = await fetch("https://bridge.frameworks.network/auth/wallet-challenge", {
  method: "POST",
  headers: { "content-type": "application/json" },
  body: JSON.stringify({ address: account.address, chain_id: 1 }),
}).then((response) => response.json());

const signature = await client.signMessage({ message: challenge.message });
```

Python (eth-account):

```python
from eth_account import Account
from eth_account.messages import encode_defunct
import os
import requests

challenge = requests.post(
    "https://bridge.frameworks.network/auth/wallet-challenge",
    json={"address": Account.from_key(os.environ["FRAMEWORKS_WALLET_PRIVKEY"]).address, "chain_id": 1},
).json()

signed = Account.sign_message(
    encode_defunct(text=challenge["message"]),
    private_key=os.environ["FRAMEWORKS_WALLET_PRIVKEY"]
)
signature = signed.signature.hex()
```

**Notes**

- Wallet headers are a one-time MCP/HTTP exchange, not reusable credentials.
- REST `POST /auth/wallet-login` sets HttpOnly auth cookies and returns user metadata.
- GraphQL `walletLogin` can exchange the same server-issued challenge, but
  clients should use bearer/API tokens for subsequent requests.
- MCP `list_linked_wallets`, `link_wallet`, and `unlink_wallet` mirror the
  authenticated GraphQL account controls and remain available at zero prepaid
  balance. Unlink locks the user row and refuses to remove a wallet-only
  account's final sign-in method unless a verified password sign-in is active,
  including under concurrent requests.

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

- `pkg/database/sql/schema` - `wallet_identities` table
- `api_control/internal/grpc` - `GetOrCreateWalletUser`, `WalletLogin`
- `pkg/auth` - EIP-191 signature verification + message validation

---

## Prepaid Balance System

Resource-based billing with prepaid credits. API and AI usage is metered and
itemized even when the current catalog prices it at zero. Delivery, bandwidth,
storage, transcoding, transcription, inference, and future model-backed work
remain separate canonical meters so pricing can change without changing the
usage contract.

Schema: `pkg/database/sql/schema` (`prepaid_balances`, `balance_transactions`)

### Enforcement

- Periscope usage summarizer runs every 5 minutes (cursor-based) and publishes usage summaries to Kafka
- Purser consumes usage summaries and deducts from `prepaid_balances`
- When balance < -$10: tenant subscription is suspended and active streams are terminated; new operations are blocked

### Top-Up Methods

1. **Card payments** - Stripe/Mollie checkout → credits balance
2. **Crypto deposits** - HD wallet address → finalized canonical receipt → credits balance
3. **x402 payments** - Gasless USDC via EIP-3009 → finalized canonical prepaid credit

New direct-deposit and x402 creation can be stopped independently with
`CRYPTO_DEPOSITS_ENABLED=false` and `X402_PAYMENTS_ENABLED=false`. Both default
to enabled. These breakers do not stop reconciliation of already-observed or
already-submitted payments.

### Key Files

- `pkg/database/sql/schema` - Balance tables
- `api_billing/internal/handlers` - Billing enforcement
- `api_billing/internal/handlers` - HD wallet derivation
- `api_billing/internal/handlers` - Deposit monitoring

### Manual custody sweep

Direct-deposit and x402 receiving addresses share
`purser.crypto_custody_addresses`. Confirmed unswept source rows move through a
four-stage platform-operator ceremony:

1. `frameworks crypto sweep plan --network <network> --out <manifest> --persist`
2. `frameworks crypto sweep sign` on an offline machine, with the matching xprv
   supplied only through descriptor 3 or higher and a versioned treasury
   allowlist, plus independent `--max-fee-gwei` and
   `--max-priority-fee-gwei` ceilings
3. `frameworks crypto sweep broadcast --bundle <bundle>` for validation, then
   `--execute --ack <bundle-checksum>`
4. `frameworks crypto sweep reconcile --batch-id <uuid> --apply`

ETH uses an offline-signed EIP-1559 raw transaction. USDC uses an
offline-signed EIP-3009 authorization and a separate online gas-relayer key;
Purser never receives the HD deposit xprv or child keys. Planning reads the
token's live EIP-712 name/version at the canonical snapshot, and broadcasting
rechecks chain, asset, source signer, amount, fees, and configured treasury.
Planning and reconciliation use the network's consensus-labelled `finalized`
head. If an RPC cannot provide it, custody operations fail closed. Allocated
deposit events remain under continuous canonicality checks; a mismatch reverses
the prepaid ledger or reopens the invoice atomically and moves the wallet to
operator review. The full two-person and interrupted-batch procedure is in
`docs/operations/sweep-ceremony.md`.

Rotate the public derivation key with
`frameworks crypto wallet rotate --xpub-file <new-xpub> --network mainnet`.
The counter never moves backwards, and every issued address retains the xpub
that created it. Sweep planning emits one signing-key group per manifest; repeat
planning until all retired-key groups are empty, and retain each retired offline
xprv until then. Rotation does not move funds automatically.

---

## x402 Protocol

FrameWorks uses the official x402 Go v2 wire types with an embedded EIP-3009
facilitator for gasless USDC `transferWithAuthorization` payments. HTTP integrations use
`PAYMENT-REQUIRED`, `PAYMENT-SIGNATURE`, and `PAYMENT-RESPONSE`; legacy v1
`X-PAYMENT` input is a compatibility fallback and is disabled by default in
production.

### How It Works

1. Authenticated client makes a rated request with insufficient balance
2. Server returns HTTP 402 with `PaymentRequirements` (payTo, asset, amount, network options)
3. Client signs EIP-3009 authorization off-chain
4. Client retries with `PAYMENT-SIGNATURE` containing the signed payload
5. Purser validates the immutable tenant quote, atomically claims it, simulates
   the exact transfer, and submits it using the dedicated gas wallet
6. Purser waits for a finalized canonical receipt containing the exact USDC
   `Transfer(from, payTo, amount)` event
7. Settlement confirmation and prepaid credit commit atomically
8. Gateway/Foghorn reload canonical billing status and run the normal
   suspension/balance policy before executing the original operation

For a side-effecting GraphQL mutation, the retry must also carry an
`Idempotency-Key` of 8–255 characters. MCP tool calls may use the same HTTP
header or `_meta.idempotencyKey`. Purser binds the key, request fingerprint,
and paid quote before execution and stores the terminal result. A completed
retry replays that result; a different fingerprint is rejected; an uncertain
in-flight outcome is never blindly executed again.

### Supported Networks

The embedded facilitator advertises enabled registry chains that pass live RPC,
USDC, relayer, custody, and finality readiness. A configured optional hosted
facilitator is instead intersected with its live `/supported` response. Network
identifiers are CAIP-2 (for example, `eip155:8453` for Base).

Each tenant has a stable HD-derived `payTo` address. This binds the signed
EIP-3009 recipient to the tenant receiving credit and prevents a captured
authorization from being claimed for another tenant. HD index 0 remains only
for recovery of legacy platform-address payments and is not advertised.

### 402 Response Format

```json
{
  "error": "insufficient_balance",
  "message": "Insufficient balance - please top up to continue",
  "code": "INSUFFICIENT_BALANCE",
  "operation": "resolveViewerEndpoint",
  "topup_url": "/account/billing",
  "x402Version": 2,
  "resource": {
    "url": "https://api.example.com/graphql",
    "description": "FrameWorks prepaid usage credit",
    "mimeType": "application/json"
  },
  "accepts": [
    {
      "scheme": "exact",
      "network": "eip155:8453",
      "amount": "5000000",
      "payTo": "0x...",
      "asset": "0x833589fCD6eDb6E08f4c7C32D4f71b54bdA02913",
      "maxTimeoutSeconds": 60,
      "extra": {
        "name": "USD Coin",
        "version": "2",
        "assetTransferMethod": "eip3009",
        "frameworks": {
          "quoteId": "<server-quote-id>",
          "resourceClass": "graphql",
          "expiresAt": "2026-08-20T12:05:00Z"
        }
      }
    }
  ]
}
```

The quote covers the tenant's current negative balance plus
`X402_PREPAID_BUFFER_EUR_CENTS`, with `X402_TOPUP_USD_CENTS` as the minimum.
The USD/EUR rate is locked into the short-lived quote. Amount, asset, CAIP-2
network, recipient, quote ID, expiry, version, and scheme must match exactly; a
positive-payment result is never an access bypass.

### Token Limitation

x402 currently settles USDC only — EIP-3009 `transferWithAuthorization` is a USDC/EURC primitive. The schema leaves room for other EIP-3009 tokens, but the current runtime network registry exposes USDC contracts only.

The non-x402 deposit flow (`CreateCryptoTopup` → HD-derived address → on-chain monitor) supports **ETH and USDC** with a price-locked quote: `expected_amount_base_units` and `quoted_price_usd` are persisted at quote time (Chainlink ETH/USD; USDC is 1:1), and the credit at confirmation is `received_amount × locked_price`. A durable per-network JSON-RPC cursor scans USDC `Transfer` logs and native transactions into `crypto_deposit_events`; event allocation, invoice/balance changes, the wallet receipt, and ledger entry commit atomically. Production requires an explicit `CRYPTO_SCAN_START_BLOCK_<NETWORK>` anchor. Native ETH sent by an internal contract transfer is not detected or accepted. **LPT** is reserved in the schema but rejected at the gate until a non-Chainlink price source is decided (no official LPT/USD aggregator).

### Testnet Support (Local Development Only)

`X402_INCLUDE_TESTNETS=true` adds Base Sepolia and Arbitrum Sepolia in local
development. Purser hard-rejects and never advertises testnets when
`BUILD_ENV=production`, even if the testnet flag is accidentally set.

### Embedded facilitator and optional hosted settlement

Production defaults to `X402_FACILITATOR_PROVIDER=self`. It requires a dedicated,
gas-only `X402_GAS_WALLET_PRIVKEY` and its matching
`X402_GAS_WALLET_ADDRESS`. Purser enforces the v2 exact EIP-3009 requirements,
including the token signing domain, canonical signature, 32-byte nonce,
settlement validity margin, simulation, durable nonce/quote claims, finalized
canonical receipt, and exact transfer event. RPC timeouts remain unknown
outcomes for reconciliation; they are never proof of failure or permission to
credit.

`hosted` and `cdp` remain optional providers selected explicitly with
`X402_FACILITATOR_PROVIDER`; CDP credentials are required only when `cdp` is
chosen. FrameWorks does not require or pay a hosted facilitator in the default
deployment.

### Authentication Boundary

x402 is payment, not login. Zero-value authorizations are rejected and payment
headers are not authentication credentials. Agents authenticate with the
wallet challenge flow or another normal credential, then use x402 to top up the
resolved tenant. Anonymous viewer payments may only target the stream owner's
resolved tenant-specific address.

### Key Files

- `api_billing/internal/handlers` - Verification + settlement
- `api_billing/internal/handlers` - Network registry
- `api_billing/internal/handlers` - Balance monitoring
- `api_gateway/internal/middleware` - standard x402 v2 HTTP headers and policy re-entry

---

## MCP Adapter

Model Context Protocol integration for AI agent tool discovery, integrated into Gateway.

The Gateway acts as an MCP **hub**: it owns the platform tools and resources
directly and proxies `ask_consultant` from the Skipper spoke. Clients should use
MCP discovery methods for the live inventory instead of relying on a hard-coded
count.

| Category                 | Tools                                                                                                                                                                                                                                                 | Resources                                                                                                                                                                            | Source        |
| ------------------------ | ----------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------ | ------------- |
| Account & Settings       | `get_tenant_settings`, `update_tenant_settings`, `update_billing_details`                                                                                                                                                                             | `account://status`                                                                                                                                                                   | Gateway       |
| Payment                  | `get_payment_options`, `submit_payment`                                                                                                                                                                                                               | —                                                                                                                                                                                    | Gateway       |
| Billing                  | `topup_balance`, `check_topup`, `pay_invoice`, `start_postpaid_setup`, `complete_mollie_postpaid_setup`                                                                                                                                               | `billing://balance`, `billing://pricing`, `billing://transactions`, `billing://invoices`, `billing://invoices/{invoice_id}`, `billing://payments`, `billing://payments/{payment_id}` | Gateway       |
| Streams & Keys           | `create_stream`, `update_stream`, `delete_stream`, `refresh_stream_key`, `list_stream_keys`, `create_stream_key`, `delete_stream_key`, `validate_stream_key`                                                                                          | `streams://list`, `streams://{id}`, `streams://{id}/health`                                                                                                                          | Gateway       |
| Multistream              | `list_push_targets`, `create_push_target`, `update_push_target`, `delete_push_target`                                                                                                                                                                 | —                                                                                                                                                                                    | Gateway       |
| Clips                    | `create_clip`, `delete_clip`                                                                                                                                                                                                                          | —                                                                                                                                                                                    | Gateway       |
| DVR                      | `start_dvr`, `stop_dvr`, `delete_dvr`                                                                                                                                                                                                                 | —                                                                                                                                                                                    | Gateway       |
| VOD                      | `create_vod_upload`, `complete_vod_upload`, `abort_vod_upload`, `get_vod_upload_status`, `delete_vod_asset`                                                                                                                                           | `vod://list`, `vod://{artifact_hash}`                                                                                                                                                | Gateway       |
| Retention                | `get_retention_policy`, `set_retention_policy`, `set_stream_retention_overrides`, `update_asset_retention`, `reset_asset_retention`                                                                                                                   | —                                                                                                                                                                                    | Gateway       |
| Playback                 | `resolve_playback_endpoint`                                                                                                                                                                                                                           | —                                                                                                                                                                                    | Gateway       |
| Playback Access Control  | `list_signing_keys`, `create_signing_key`, `revoke_signing_key`, `set_playback_policy`, `clear_playback_policy`, `test_playback_access`                                                                                                               | —                                                                                                                                                                                    | Gateway       |
| Analytics                | —                                                                                                                                                                                                                                                     | `analytics://usage`, `analytics://viewers`, `analytics://geographic`, `analytics://routing`, `analytics://federation`, `analytics://network-topology`                                | Gateway       |
| QoE Diagnostics          | 6 tools (`diagnose_*`, `get_stream_health_summary`, `get_anomaly_report`)                                                                                                                                                                             | —                                                                                                                                                                                    | Gateway       |
| Support                  | `list_support_conversations`, `search_support_history`                                                                                                                                                                                                | `support://conversations`, `support://conversations/{conversation_id}`                                                                                                               | Gateway       |
| Knowledge                | `ask_consultant`                                                                                                                                                                                                                                      | `knowledge://sources`                                                                                                                                                                | Skipper spoke |
| Schema                   | `introspect_schema`, `generate_query`, `execute_query`                                                                                                                                                                                                | `schema://catalog`                                                                                                                                                                   | Gateway       |
| Infrastructure           | `browse_marketplace`, `subscribe_to_cluster`, `unsubscribe_from_cluster`, `set_preferred_cluster`, `update_cluster_marketplace`, `create_edge_cluster`, `create_enrollment_token`, `get_node_info`, `manage_node`, `set_node_mode`, `get_node_health` | `nodes://list`, `nodes://{id}`, `clusters://list`, `clusters://{id}`, `clusters://marketplace`                                                                                       | Gateway       |
| Cluster Invites & Access | `create_cluster_invite`, `revoke_cluster_invite`, `accept_cluster_invite`, `request_cluster_subscription`, `approve_subscription_request`, `reject_subscription_request`                                                                              | —                                                                                                                                                                                    | Gateway       |

Code: `api_gateway/internal/mcp/` (tools, resources, prompts, preflight), `api_consultant/internal/` (mcpclient, mcpspoke, chat orchestrator). For full tool parameters, see the [public docs](https://logbook.frameworks.network/agents/mcp/).

### Preflight Checks

Before billable operations, the preflight checker validates:

1. Authentication (tenant_id in context)
2. Billing details (required before billable operations)
3. Prepaid available balance (settled balance minus active reservations; positive available balance required)

**Note**: x402 settlement requires full billing details above €100; exactly
€100 remains within the simplified-record boundary.

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

## Node Provisioning

Agents can provision and manage their own edge nodes. The full flow:

1. **Create an edge cluster** via MCP `create_edge_cluster` — returns a bootstrap enrollment token and Foghorn address
2. **Generate additional tokens** via `create_enrollment_token` for the same cluster
3. **Provision edge nodes** with the CLI:
   ```
   frameworks edge provision --enrollment-token <token> --ssh user@host
   ```
4. **Monitor node health** via `get_node_info` (static registration data) or `get_node_health` (live metrics from Foghorn)
5. **Manage node lifecycle** — two paths:
   - **CLI path** (local or SSH): `frameworks edge mode draining`, `frameworks edge mode maintenance`, `frameworks edge mode normal`
   - **API path** (no SSH needed): `set_node_mode` MCP tool → Gateway → Commodore → Foghorn
6. **Guided CLI** via `manage_node` — returns CLI commands for status, diagnose, logs

### Two Management Paths

| Path | Flow                                                 | Use case                              |
| ---- | ---------------------------------------------------- | ------------------------------------- |
| CLI  | Agent → Helmsman HTTP → Foghorn control stream       | Agent has shell access (local or SSH) |
| API  | Agent → Gateway MCP → Commodore proxy → Foghorn gRPC | Agent has no shell access to the edge |

Both paths update state in Foghorn's Redis-backed state store. The routing effect (stop assigning new viewers) is immediate across all Foghorn instances via Redis pub/sub. The CLI path additionally pushes a `ConfigSeed` to Helmsman over the bidirectional control stream.

### Key Files

- `cli/cmd` — CLI commands (`provision`, `mode`, `status`, `doctor`, `logs`)
- `api_sidecar/internal/handlers` — Helmsman HTTP endpoints (`/node/mode`)
- `api_sidecar/internal/control` — Upstream mode change via control stream
- `api_balancing/internal/control` — Foghorn handler for `ModeChangeRequest`
- `api_balancing/internal/grpc` — Foghorn `NodeControlService` (SetNodeOperationalMode, GetNodeHealth)
- `api_control/internal/grpc` — Commodore `NodeManagementService` proxy
- `api_gateway/internal/mcp/tools` — MCP tools

---

## x402 invoice records and VAT limitations

### Simplified Invoice Rule (x402)

- Confirmed x402 top-ups generate internal simplified-invoice records in
  `purser.simplified_invoices`.
- Payments **over €100** are blocked unless billing details are present.
- Full VAT invoice generation for x402 payments is **not** implemented.
- A VAT-number format check never enables reverse charge. Purser calls the
  official VIES `checkVat` service, caches the result for 24 hours, and persists
  a hashed/masked validation record. Reverse charge requires both a current
  valid VIES result and a customer country different from `SUPPLIER_COUNTRY`;
  a domestic VAT identifier receives domestic VAT.
- Confirmed top-up is the recorded VAT tax point. Later usage consumes the
  already-invoiced balance and does not create another VAT event.
- x402 readiness requires supplier name, address, VAT number, and ISO country;
  production also requires an official facilitator and scanner anchors.

The record stores billing country and GeoIP country separately, labels the
evidence `complete`, `single_source`, `conflict`, or `missing`, and never treats
the settlement network as customer-location evidence. Conflicts remain visible
for accounting review and prevent an honest production sign-off until the
operator's evidence policy covers them.

Schema: `pkg/database/sql/schema` (`simplified_invoices`)

Configuration: See the dev compose configuration and environment files under `config/env`.

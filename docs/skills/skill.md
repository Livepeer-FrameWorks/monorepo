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

FrameWorks is a multi-tenant live streaming platform. **MCP is the primary interface** for agent access.

## Skill Files

| File          | URL                                                 |
| ------------- | --------------------------------------------------- |
| skill.md      | https://frameworks.network/skill.md                 |
| skill.json    | https://frameworks.network/skill.json               |
| MCP discovery | https://api.frameworks.network/.well-known/mcp.json |

## Security Notes

- Never share private keys or seed phrases with third parties.
- Store agent credentials locally (example: `~/.config/frameworks/credentials.json`).
- Only send authentication headers to `*.frameworks.network` domains.

## Quick Start (Agent Flow)

1. **Create or load an EVM wallet.**
2. **Sign a wallet login message** (EIP-191) and call `/auth/wallet-login` to auto-provision a prepaid tenant.
3. **Fund the tenant** via x402 or crypto deposit.
4. **Create a stream** using MCP `create_stream`.
5. **Push RTMP** using the stream key returned by MCP/GraphQL.

## MCP (Primary Interface)

- Discovery: `GET /.well-known/mcp.json`
- Endpoint: `POST /mcp`
- Transport: HTTP + SSE

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

## x402 Payments (Anti-Abuse)

x402 provides gasless USDC payments for instant top-ups or per-request auth. It also acts as an economic barrier against automated abuse.

- Header: `X-PAYMENT: <base64 payload>`
- Supported networks: Base, Arbitrum (USDC)

## GraphQL (Alternative Interface)

Endpoint: `POST /graphql`

Key operations:

- Mutations: `createStream`, `updateStream`, `deleteStream`, `refreshStreamKey`
- Queries: `streams`, `stream`, `me`

Authentication: same wallet headers or bearer token.

## Rate Limits & Billing

- API requests are free; usage costs apply to viewer hours, storage, and processing.
- Prepaid balance must be positive to run billable operations.
- Use MCP `billing://balance` to monitor balance and drain rate.

## Heartbeat Checklist

Run periodically to keep the agent in a healthy state:

1. Read `account://status`.
2. Check `billing://balance`.
3. Review `streams://list` and `streams://{id}/health`.
4. Refresh stream keys if compromise is suspected.

## Common Errors

| Code                      | Cause                   | Resolution                         |
| ------------------------- | ----------------------- | ---------------------------------- |
| `AUTHENTICATION_REQUIRED` | Missing auth headers    | Send wallet or bearer auth headers |
| `BILLING_DETAILS_MISSING` | Billing address missing | Call `update_billing_details`      |
| `INSUFFICIENT_BALANCE`    | Balance is 0            | Pay via x402 or `topup_balance`    |

## Example Flow (Create + Go Live)

1. Call `account://status` to confirm readiness.
2. Call `get_payment_options` or `topup_balance` if balance is low.
3. Call `create_stream` and capture `stream_key` + `rtmp_url`.
4. Push RTMP to start streaming.
5. Monitor `streams://{id}/health` for QoE.

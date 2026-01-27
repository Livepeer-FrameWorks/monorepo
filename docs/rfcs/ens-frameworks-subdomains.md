# RFC: FrameWorks ENS Offchain Subdomains & Donations

## Status

Draft

## TL;DR

- FrameWorks issues gasless ENS subdomains (`alice.frameworks.eth`) via CCIP-Read.
- Subdomains map to HD-derived wallet addresses for donations.
- Donations credit stream balances (ties to stream-balances RFC).

## Current State (as of 2026-01-26)

**Existing infrastructure:**

- HD wallet derivation: `api_billing/internal/handlers/hdwallet.go`
- Crypto deposit detection: `api_billing/internal/handlers/checkout.go`
- Stream balances RFC: `docs/rfcs/stream-balances.md` (Draft)

**Missing:**

- ENS subdomain resolver
- Subdomain → HD address mapping
- ENS text record population

Evidence:

- [Source] HD wallet implementation: `api_billing/internal/handlers/hdwallet.go`
- [Source] Stream balances RFC: `docs/rfcs/stream-balances.md`
- [Reference] ENS Offchain Resolution (ENSIP-16): https://docs.ens.domains/ensip/16

## Problem / Motivation

- Users want web3 identity without buying ENS names (gas costs).
- Creators want to receive donations via human-readable addresses.
- Donations should fund stream infrastructure costs.

## Goals

- Issue gasless subdomains to FrameWorks users.
- Map subdomains to HD-derived deposit addresses.
- Credit donations to stream/creator balances.
- Populate streaming text records on subdomains.

## Non-Goals

- Managing user-owned ENS names (they control their own records).
- Building a general-purpose ENS resolver (only FrameWorks subdomains).

## Proposal

### Subdomain Model

```
alice.frameworks.eth           → creator-level (wallet, default stream)
stream1.alice.frameworks.eth   → stream-specific (balance, endpoints)
gaming.alice.frameworks.eth    → channel-specific subdomain
```

**Creator subdomains** (`alice.frameworks.eth`):

- Linked to user's wallet address
- Default stream endpoints
- Receives donations for the user

**Stream subdomains** (`stream1.alice.frameworks.eth`):

- Specific to a single stream
- Stream-specific playback URLs
- Donations credit that stream's balance

### Offchain Resolver Architecture

FrameWorks owns `frameworks.eth` and configures an offchain resolver using CCIP-Read (EIP-3668).

**Resolution flow:**

```
1. Wallet queries alice.frameworks.eth
2. ENS sees offchain resolver, returns CCIP-Read URL
3. Wallet calls FrameWorks resolver API
4. Resolver looks up subdomain in database
5. Returns records (wallet address, streaming text records)
```

**Resolver API endpoints:**

```
GET /ens/resolve/{name}
  → Returns all text records for the subdomain

POST /ens/ccip-read
  → CCIP-Read compliant endpoint for wallets
  → Body: { sender, data } (EIP-3668 format)
  → Returns: ABI-encoded response
```

### HD Wallet Mapping

Each subdomain gets a unique deposit address derived from the platform HD wallet.

**Derivation:**

```
xpub (platform HD wallet)
  ↓
Subdomain registered → get next derivation index
  ↓
Derive address at index (BIP44: m/44'/60'/0'/0/{index})
  ↓
Store mapping: subdomain → address → stream/creator
```

**Database schema addition:**

```sql
CREATE TABLE purser.ens_subdomains (
  id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  subdomain TEXT NOT NULL UNIQUE,           -- "alice" or "stream1.alice"
  tenant_id UUID NOT NULL REFERENCES quartermaster.tenants(id),
  stream_id UUID REFERENCES commodore.streams(id),  -- NULL for creator-level
  wallet_address TEXT NOT NULL,              -- HD-derived address
  derivation_index INT NOT NULL,
  created_at TIMESTAMPTZ DEFAULT NOW()
);
```

### Donation Flow

```
1. User queries alice.frameworks.eth from wallet (MetaMask, Rainbow, etc.)
2. Wallet resolves ENS → gets HD-derived address
3. User sends ETH/USDC to that address
4. On-chain indexer detects deposit (existing crypto topup infra)
5. Lookup subdomain by address → find stream/creator
6. Credit to stream balance (or creator balance if no stream)
7. Deduct infrastructure costs, share excess with creator
```

This reuses existing infrastructure:

- `api_billing/internal/handlers/hdwallet.go` - address derivation
- `api_billing/internal/handlers/checkout.go` - deposit detection
- Stream balances (per stream-balances RFC)

### Text Record Population

When a subdomain is created, populate streaming text records:

```go
func PopulateSubdomainRecords(subdomain string, stream *Stream) map[string]string {
  return map[string]string{
    "com.livepeer.playbackId": stream.PlaybackID,
    "com.livepeer.gateway":    config.GatewayPublicURL + "/graphql",
    "com.livepeer.stream":     config.StreamingPlayURL + "/play/" + stream.PlaybackID + "/index.m3u8",
    "com.livepeer.whep":       config.StreamingPlayURL + "/play/" + stream.PlaybackID + ".webrtc",
  }
}
```

For creator-level subdomains without a specific stream, use their default/primary stream.

### Integration with Stream Balances RFC

The stream-balances RFC defines per-stream balances. ENS donations integrate as a funding source:

1. Donation arrives at HD-derived address.
2. Lookup finds associated stream.
3. Credit stream balance (same as crypto topup).
4. Stream balance pays for infrastructure usage.
5. Excess shared with creator (per stream-balances RFC rules).

## Impact / Dependencies

| Component            | Change Required                                  |
| -------------------- | ------------------------------------------------ |
| api_billing (Purser) | ENS subdomain table, resolver API                |
| api_gateway (Bridge) | GraphQL mutations for subdomain management       |
| pkg/database         | Schema migration for ens_subdomains              |
| ENS                  | Configure frameworks.eth with CCIP-Read resolver |

## Alternatives Considered

**On-chain subdomains**: Rejected due to gas costs for users. Offchain via CCIP-Read is gasless.

**Shared deposit address**: Rejected because we need to attribute deposits to specific creators/streams.

## Risks & Mitigations

| Risk                        | Mitigation                                                     |
| --------------------------- | -------------------------------------------------------------- |
| Resolver downtime           | Wallets retry; fallback to cached records                      |
| Address substitution attack | HD derivation is deterministic; all addresses logged for audit |
| Subdomain squatting         | Registration requires FrameWorks account; rate limits          |

## Migration / Rollout

1. Deploy resolver API in api_billing.
2. Configure frameworks.eth with CCIP-Read resolver.
3. Add GraphQL mutations for subdomain registration.
4. Update stream creation to auto-create subdomains.
5. Enable donation detection for ENS addresses.

## Open Questions

- Should subdomain registration be automatic (on stream creation) or opt-in?
- What's the subdomain naming policy? (alphanumeric, length limits, reserved names)
- How to handle subdomain conflicts across tenants?

## References, Sources & Evidence

- [Source] HD wallet: `api_billing/internal/handlers/hdwallet.go`
- [Source] Crypto deposits: `api_billing/internal/handlers/checkout.go`
- [Source] Stream balances RFC: `docs/rfcs/stream-balances.md`
- [Reference] CCIP-Read (EIP-3668): https://eips.ethereum.org/EIPS/eip-3668
- [Reference] ENS Offchain Resolution (ENSIP-16): https://docs.ens.domains/ensip/16

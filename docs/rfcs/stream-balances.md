# RFC: Stream-Level Balances

## Status
Draft

## TL;DR
- Add optional per-stream balances that can fund usage before tenant balance.
- Enable pay-per-view, creator tips, and sponsor-funded streams.
- Default behavior remains tenant-only unless enabled per stream.

## Current State (as of 2026-01-13)
- Billing is tenant-level only (prepaid balances in Purser).
- No stream-specific balance tables or APIs in the schema.

Evidence:
- `pkg/database/sql/schema/purser.sql`
- `api_billing/`

## Problem / Motivation
Tenant-only balances block new revenue models (pay-per-view, tips, per-stream cost allocation, agent sponsorship). We need a per-stream balance without breaking existing billing.

## Goals
- Optional stream-level balances with configurable priority.
- Backward-compatible default behavior.
- Clear audit trail per stream and tenant.

## Non-Goals
- Replacing tenant balances.
- Building full payout/creator revenue distribution in v1.

## Proposal
- Add stream balance tables (balance + transactions).
- Add `billing_priority` per stream: `stream_first` (default), `tenant_only`, `stream_only`.
- Allow public funding toggle per stream.
- Stream deletion transfers remaining balance to tenant.

### ENS Donations as Funding Source

ENS subdomains provide a human-readable way for viewers to fund streams. See `docs/rfcs/ens-frameworks-subdomains.md` for full implementation.

**Flow:**
1. Creator has `alice.frameworks.eth` or stream has `stream1.alice.frameworks.eth`.
2. Subdomain resolves to HD-derived deposit address (via CCIP-Read).
3. Viewer sends ETH/USDC to that address from any wallet.
4. Existing crypto deposit detection credits the stream balance.
5. Stream balance pays infrastructure costs first.
6. Excess is available for creator withdrawal (per tenant payout settings).

**Integration points:**
- `api_billing/internal/handlers/hdwallet.go` - address derivation
- `api_billing/internal/handlers/checkout.go` - deposit detection
- New `purser.ens_subdomains` table maps subdomain → stream → address

**Revenue share:**
- Infrastructure costs deducted at platform rates.
- Remaining balance accrues to stream (or creator if no stream specified).
- Creator withdrawal follows standard payout flow.

## Impact / Dependencies
- Purser schema and billing jobs.
- GraphQL mutations/queries.
- Foghorn enforcement (suspend stream on `stream_only` depletion).
- x402 for stream-targeted funding.

## Alternatives Considered
- Separate "tips" ledger without affecting billing.
- Per-tenant sub-accounts instead of per-stream balances.

## Risks & Mitigations
- Risk: race conditions in balance deductions. Mitigation: transactional updates + idempotent usage records.
- Risk: abuse via public funding. Mitigation: caps, rate limits, and audit trails.

## Migration / Rollout
1. Add schema + read APIs.
2. Add funding mutations.
3. Add billing enforcement rules.
4. Rollout per-tenant feature flag.

## Open Questions
- Should public funding be default off or on?
- How to reconcile negative balances for `stream_only`?

## References, Sources & Evidence
- `pkg/database/sql/schema/purser.sql`
- `api_billing/`
- `pkg/graphql/schema.graphql`
- `docs/rfcs/ens-frameworks-subdomains.md` - ENS subdomain + donation implementation

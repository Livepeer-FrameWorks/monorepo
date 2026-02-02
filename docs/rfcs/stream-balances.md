# RFC: Stream-Level Balances

## Status

Draft

## TL;DR

- Add optional per-stream balances that can fund usage before tenant balance.
- Enable pay-per-view, creator tips, and sponsor-funded streams.
- Default behavior remains tenant-only unless enabled per stream.
- **Promotional system**: Promo codes, referral program, volume discounts.

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

### Promotional Credits System

Beyond stream-level balances, we need tenant-level promotional mechanisms.

#### Promo Codes

Allow tenants to redeem codes for discounts or credits.

```sql
CREATE TABLE purser.promo_codes (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    code TEXT UNIQUE NOT NULL,           -- 'LAUNCH2025'
    discount_type TEXT NOT NULL,         -- 'percentage', 'fixed_amount', 'credit'
    discount_value DECIMAL(10,2) NOT NULL, -- 25 (%) or 100.00 ($)
    target TEXT NOT NULL,                -- 'subscription', 'overage', 'all'
    conditions JSONB,                    -- {"first_n_months": 3, "min_tier": "supporter"}
    usage_limit INT,                     -- NULL = unlimited
    usage_count INT DEFAULT 0,
    valid_from TIMESTAMPTZ,
    valid_until TIMESTAMPTZ,
    created_at TIMESTAMPTZ DEFAULT NOW()
);

CREATE TABLE purser.promo_redemptions (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    promo_code_id UUID REFERENCES purser.promo_codes(id),
    tenant_id UUID NOT NULL,
    redeemed_at TIMESTAMPTZ DEFAULT NOW(),
    UNIQUE(promo_code_id, tenant_id)     -- One redemption per tenant
);
```

**Use cases:**

- `LAUNCH2025`: 25% off first 3 months
- `CONFERENCE`: $50 credit for conference attendees
- `PARTNER_ACME`: Custom rates for specific partner

**GraphQL:**

```graphql
extend type Mutation {
  promoCodeRedeem(code: String!): PromoCodeRedeemPayload!
}

type PromoCodeRedeemPayload {
  success: Boolean!
  credit: PrepaidCredit
  discount: AppliedDiscount
  userErrors: [UserError!]!
}
```

#### Referral Program

Existing tenants refer new tenants; both receive credit.

```sql
CREATE TABLE purser.referrals (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    referrer_tenant_id UUID NOT NULL,
    referred_tenant_id UUID NOT NULL,
    referral_code TEXT NOT NULL,         -- Unique per referrer or tenant slug
    status TEXT DEFAULT 'pending',       -- 'pending', 'qualified', 'credited'
    referrer_credit DECIMAL(10,2),       -- Credit for referrer
    referee_credit DECIMAL(10,2),        -- Credit for new tenant
    qualified_at TIMESTAMPTZ,            -- When referee met criteria
    created_at TIMESTAMPTZ DEFAULT NOW(),
    UNIQUE(referred_tenant_id)           -- Each tenant can only be referred once
);
```

**Qualification criteria** (configurable):

- First invoice paid
- N days of active usage
- Upgraded from free tier

**Flow:**

1. Tenant generates referral code (or uses default `tenant_slug`)
2. New tenant signs up with referral code in URL or during onboarding
3. New tenant completes qualification criteria
4. Both parties receive credit to prepaid balance
5. Notification sent to both

**GraphQL:**

```graphql
extend type Query {
  referralCode: String! # Get my referral code
  referralStats: ReferralStats! # My referral history
}

type ReferralStats {
  totalReferred: Int!
  pendingCount: Int!
  creditedCount: Int!
  totalEarned: Money!
}
```

#### Volume Discounts

Auto-apply discounts when usage exceeds thresholds.

```sql
CREATE TABLE purser.volume_discount_rules (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    tier_id UUID REFERENCES purser.billing_tiers(id),  -- NULL = all tiers
    metric TEXT NOT NULL,                -- 'delivered_minutes', 'storage_gb', 'gpu_hours'
    threshold BIGINT NOT NULL,           -- 1000000 (1M minutes)
    discount_percentage DECIMAL(5,2),    -- 10.00 (%)
    applies_to TEXT NOT NULL,            -- 'overage', 'base', 'all'
    created_at TIMESTAMPTZ DEFAULT NOW()
);
```

**Examples:**

- Production tier: 10% off overages after 2M delivered minutes
- All tiers: 5% off storage after 500GB
- Enterprise: Custom volume breaks

**Calculation:**

- Applied automatically during invoice generation
- Shown as line item discount
- Stacks with promo codes (configurable)

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

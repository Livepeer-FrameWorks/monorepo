# Billing Tier Provisioning - Account Creation & Cluster Access

Billing tiers drive cluster access. When an account is created or promoted, the billing tier determines which platform clusters the tenant can access. Purser orchestrates both billing subscription creation and cluster provisioning via Quartermaster.

## Architecture

```
                    ┌─────────────┐
                    │  Commodore  │
                    │  (Register) │
                    └──────┬──────┘
                           │ InitializePostpaidAccount
                           ▼
                    ┌─────────────┐       ┌────────────────┐
                    │   Purser    │──────▶│  Quartermaster  │
                    │             │       │                 │
                    │ 1. Resolve  │       │ SubscribeToCluster
                    │    tier     │       │ UpdateTenant     │
                    │ 2. Create   │       │  (primary_cluster)│
                    │    sub      │       └────────────────┘
                    │ 3. Cluster  │
                    │    access   │
                    └─────────────┘
```

## Service Responsibilities

| Service       | Role                                                     | Data                                                       |
| ------------- | -------------------------------------------------------- | ---------------------------------------------------------- |
| Commodore     | Triggers billing init during Register                    | Calls Purser after user creation                           |
| Purser        | Resolves tier, creates subscription, provisions clusters | `billing_tiers`, `tenant_subscriptions`, `cluster_pricing` |
| Quartermaster | Manages cluster subscriptions and primary cluster        | `tenant_cluster_access`, `tenants.primary_cluster_id`      |

## Data Flows

### New Email Account (Postpaid)

```
1. Commodore.Register creates tenant via Quartermaster
2. Commodore calls Purser.InitializePostpaidAccount(tenant_id)
3. Purser resolves tier WHERE is_default_postpaid = true
4. Purser creates subscription (billing_model=postpaid)
5. Purser.ensureTierClusterAccess:
   a. Queries cluster_pricing for eligible platform clusters
   b. Subscribes tenant to each via Quartermaster.SubscribeToCluster
   c. Sets highest-tier-level cluster as primary via Quartermaster.UpdateTenant
```

### New Wallet Account (Prepaid)

```
1. Commodore.GetOrCreateWalletUser creates tenant via Quartermaster
2. Commodore calls Purser.InitializePrepaidAccount(tenant_id, currency)
3. Purser resolves tier WHERE is_default_prepaid = true
4. Purser creates subscription (billing_model=prepaid) + prepaid balance
5. Purser.ensureTierClusterAccess provisions clusters (same as above)
```

### Prepaid → Postpaid Promotion

```
1. Gateway calls Purser.PromoteToPaid(tenant_id)
2. Purser verifies current billing_model = prepaid
3. Purser resolves tier WHERE is_default_postpaid = true
4. Purser updates subscription (billing_model → postpaid, new tier)
5. Purser.ensureTierClusterAccess re-evaluates cluster access
6. Prepaid balance is carried forward as credit
```

## Default Tier Configuration

Default tiers are configured via boolean flags on `purser.billing_tiers`:

| Flag                  | Meaning                                 | Current default  |
| --------------------- | --------------------------------------- | ---------------- |
| `is_default_prepaid`  | Assigned to wallet/x402 accounts        | `payg` (level 0) |
| `is_default_postpaid` | Assigned to email registration accounts | `free` (level 1) |

Exactly one tier should have each flag set to `true`.

## Cluster Eligibility

`ensureTierClusterAccess` queries `purser.cluster_pricing` for platform clusters the tier can access:

```sql
WHERE is_platform_official = true
  AND required_tier_level <= <tier_level>
  AND (allow_free_tier = true OR <tier_level> > 0)
```

The cluster with the highest `required_tier_level` is set as primary (most capable cluster the tier grants access to).

## Key Files

- `api_billing/internal/grpc` - `ensureTierClusterAccess`, `InitializePrepaidAccount`, `InitializePostpaidAccount`, `PromoteToPaid`
- `api_control/internal/grpc` - `Register` calls `InitializePostpaidAccount`
- `pkg/proto` - RPC definitions and response messages
- `pkg/database/sql/schema` - `billing_tiers` (with default flags), `cluster_pricing`
- `pkg/database/sql/seeds/static` - Default flag assignments

## Gotchas

- Quartermaster's `CreateTenant` still auto-subscribes to `is_default_cluster=true` clusters as a safety net. Purser's cluster subscription uses `ON CONFLICT DO NOTHING` so the overlap is harmless.
- Paid tier upgrades (Stripe/Mollie checkout) are blocked at the Gateway level (`api_gateway/internal/resolvers`). The Purser RPCs themselves remain unrestricted for admin operations.
- `PromoteToPaid` ignores the caller's `tier_id` and always resolves the default postpaid tier.

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
                    │ 1. Resolve  │       │ BootstrapClusterAccess
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
| Quartermaster | Materializes tier-derived grants and primary routing     | `tenant_cluster_access`, `tenants.primary_cluster_id`      |

## Data Flows

### New Email Account (Postpaid)

```
1. Commodore.Register creates tenant via Quartermaster
2. Commodore calls Purser.InitializePostpaidAccount(tenant_id)
3. Purser resolves tier WHERE is_default_postpaid = true
4. Purser creates subscription (billing_model=postpaid)
5. Purser.ensureTierClusterAccess:
   a. Queries cluster_pricing for eligible platform clusters
   b. Materializes `platform_tier` access via Quartermaster.BootstrapClusterAccess
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
1. Gateway calls `Purser.PromoteToPaid(tenant_id, optional tier_id)`.
2. Purser starts a transaction and locks the tenant subscription `FOR UPDATE`.
3. An explicit active postpaid-eligible tier is honored; otherwise Purser
   resolves `is_default_postpaid = true`.
4. Free needs verified email but no postal profile/provider. Paid postpaid tiers
   require complete billing identity and confirmed Stripe/Mollie collection.
5. Purser commits prepaid → postpaid while retaining prepaid credit.
6. Purser rereads the canonical committed tier, then reconciles clusters and
   invalidates caches. A same-target retry is idempotent.
```

## Default Tier Configuration

Default tiers are configured via boolean flags on `purser.billing_tiers`:

| Flag                  | Meaning                                 | Current default  |
| --------------------- | --------------------------------------- | ---------------- |
| `is_default_prepaid`  | Assigned to wallet/x402 accounts        | `payg` (level 0) |
| `is_default_postpaid` | Assigned to email registration accounts | `free` (level 1) |

Exactly one tier should have each flag set to `true`.

## Cluster Eligibility

`ensureTierClusterAccess` first asks Quartermaster for platform-official cluster IDs, then filters `purser.cluster_pricing` for clusters the tier can access:

```sql
WHERE cluster_id = ANY(<quartermaster_official_cluster_ids>)
  AND required_tier_level <= <tier_level>
  AND (allow_free_tier = true OR <tier_level> > 0)
```

Purser grants each eligible cluster through Quartermaster's service-token `BootstrapClusterAccess` RPC. The cluster with the highest `required_tier_level` is set as primary (most capable cluster the tier grants access to).

## Key Files

- `api_billing/internal/grpc` - `ensureTierClusterAccess`, `InitializePrepaidAccount`, `InitializePostpaidAccount`, `PromoteToPaid`
- `api_control/internal/grpc` - `Register` calls `InitializePostpaidAccount`
- `pkg/proto` - RPC definitions and response messages
- `pkg/database/sql/schema` - `billing_tiers` (with default flags), `cluster_pricing`
- `pkg/database/sql/seeds/static` - Default flag assignments

## Gotchas

- Quartermaster's `CreateTenant` still auto-subscribes to the single `is_default_cluster=true` cluster as a safety net and sets `official_cluster_id`. Purser's later cluster provisioning is idempotent, so overlap is harmless.
- Tier-specific paid collection must be completed before selecting a non-Free
  postpaid tier. Free activation remains provider-free.
- `PromoteToPaid` honors an explicit active, postpaid-eligible `tier_id`; an
  empty value selects the default postpaid tier.

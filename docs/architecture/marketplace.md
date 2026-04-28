# Cluster Marketplace - Multi-tenant cluster discovery and subscription

Tenants browse, subscribe to, and connect to streaming clusters operated by third-party
operators or the platform itself. Cluster owners control visibility and access approval;
Bridge coordinates pricing updates through Purser.

## Architecture

```
Tenant (UI)                     Bridge (GraphQL)        Quartermaster (gRPC)        Purser (gRPC)
  │                                │                        │                          │
  │ marketplaceClustersConnection  │                        │                          │
  │───────────────────────────────→│ ListMarketplaceClusters│                          │
  │                                │───────────────────────→│                          │
  │                                │← visibility/access     │                          │
  │                                │ GetClustersPricingBatch│                          │
  │                                │─────────────────────────────────────────────────→│
  │← list with pricing, eligibility, subscription status                              │
  │                                │                        │                          │
  │ requestClusterSubscription     │                        │                          │
  │───────────────────────────────→│ RequestClusterSubscription                        │
  │                                │───────────────────────→│                          │
  │← pending_approval / active     │                        │                          │
  │                                │                        │                          │
Operator (UI)                      │                        │                          │
  │ updateClusterMarketplace       │ UpdateClusterMarketplace                         │
  │───────────────────────────────→│───────────────────────→│                          │
  │                                │ SetClusterPricing                                 │
  │                                │─────────────────────────────────────────────────→│
  │                                │                        │                          │
  │ approveClusterSubscription     │                        │                          │
  │───────────────────────────────→│ ApproveClusterSubscription                        │
  │                                │───────────────────────→│                          │
```

## Service Responsibilities

| Service          | Role                                                        | Data                                                                  |
| ---------------- | ----------------------------------------------------------- | --------------------------------------------------------------------- |
| Quartermaster    | Cluster discovery, access metadata, subscription lifecycle  | `infrastructure_clusters`, `tenant_cluster_access`, `cluster_invites` |
| Purser           | Per-cluster pricing configuration and eligibility           | `cluster_pricing`, tenant billing tiers                               |
| Bridge (Gateway) | GraphQL resolvers, union-type error handling, pricing merge | Proxies to Quartermaster and Purser                                   |
| SvelteKit UI     | Marketplace browse, connect, request access                 | `website_application/src/routes/infrastructure/marketplace/`          |

## Data Model

### Cluster visibility

- `PUBLIC` — listed in marketplace, discoverable by all tenants
- `UNLISTED` — accessible via direct link or invite only
- `PRIVATE` — invite-only, not listed

### Pricing models

- `FREE_UNMETERED` — no charge
- `METERED` — usage-based billing
- `MONTHLY` — fixed monthly subscription (price in cents)
- `TIER_INHERIT` — follows tenant's billing tier
- `CUSTOM` — operator-defined

### Subscription lifecycle

```
Tenant requests access
  │
  ├─ requiresApproval = false → ACTIVE (immediate)
  │
  └─ requiresApproval = true  → PENDING_APPROVAL
                                   │
                                   ├─ Operator approves → ACTIVE
                                   ├─ Operator rejects  → REJECTED
                                   └─ Operator suspends → SUSPENDED
```

### Tenant-cluster binding

Streams inherit the tenant's preferred cluster (set via `SetPreferredCluster` mutation).
No per-stream cluster override exists.

## GraphQL Operations

**Queries:**

- `marketplaceClustersConnection` — paginated cluster browse with eligibility + subscription status
- `marketplaceCluster(clusterId)` — single cluster detail
- `pendingSubscriptionsConnection(clusterId)` — operator view of pending requests
- `clusterInvitesConnection(clusterId)` — invite management

**Mutations:**

- `requestClusterSubscription(clusterId, inviteToken?)` — tenant subscribes or requests access
- `approveClusterSubscription(subscriptionId)` — operator approves
- `rejectClusterSubscription(subscriptionId, reason?)` — operator rejects
- `updateClusterMarketplace(clusterId, input)` — operator updates visibility/approval in Quartermaster and pricing fields in Purser
- `createClusterInvite(input)` / `revokeClusterInvite(inviteId)` / `acceptClusterInvite(inviteToken)` — invite-based access
- `setPreferredCluster(clusterId)` — tenant sets default cluster for new streams

## Key Files

- `pkg/graphql` — `MarketplaceCluster`, `ClusterSubscription`, `ClusterVisibility`, `ClusterPricingModel`, `ClusterSubscriptionStatus` types
- `api_gateway/internal/resolvers` — resolver implementations
- `api_tenants/internal/grpc` — Quartermaster RPC handlers (`RequestClusterSubscription`, `ApproveClusterSubscription`, etc.)
- `api_billing/internal/grpc` — Purser cluster pricing handlers (`GetClustersPricingBatch`, `SetClusterPricing`, `GetClusterPricing`)
- `pkg/proto` — `ListMarketplaceClusters`, `RequestClusterSubscription`, `ApproveClusterSubscription`, `RejectClusterSubscription`, `SetClusterPricing` RPCs
- `pkg/database/sql/schema` — `quartermaster.tenant_cluster_access`, `quartermaster.cluster_invites`, `purser.cluster_pricing` tables
- `website_application/src/routes/infrastructure/marketplace` — marketplace UI

## Future Work

- Cluster SLA enforcement across operators
- Minimum compliance bar for third-party clusters
- Operator onboarding documentation / self-serve flow

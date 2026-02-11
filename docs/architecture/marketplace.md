# Cluster Marketplace - Multi-tenant cluster discovery and subscription

Tenants browse, subscribe to, and connect to streaming clusters operated by third-party
operators or the platform itself. Cluster owners control visibility, pricing, and
access approval.

## Architecture

```
Tenant (UI)                     Bridge (GraphQL)              Quartermaster (gRPC)
  │                                │                              │
  │ GetMarketplaceClusters         │                              │
  │───────────────────────────────→│ ListMarketplaceClusters      │
  │                                │─────────────────────────────→│
  │                                │← clusters + eligibility      │
  │← cluster list with pricing,   │                              │
  │   eligibility, subscription   │                              │
  │   status                      │                              │
  │                                │                              │
  │ RequestClusterSubscription     │                              │
  │───────────────────────────────→│ RequestClusterSubscription   │
  │                                │─────────────────────────────→│
  │                                │← subscription record         │
  │← pending / active             │                              │
  │                                │                              │
  │                                │                              │
Operator (UI)                      │                              │
  │ ApproveClusterSubscription     │                              │
  │───────────────────────────────→│ ApproveClusterSubscription   │
  │                                │─────────────────────────────→│
  │                                │← approved subscription       │
```

## Service Responsibilities

| Service          | Role                                                       | Data                                                         |
| ---------------- | ---------------------------------------------------------- | ------------------------------------------------------------ |
| Quartermaster    | Cluster discovery, access metadata, subscription lifecycle | `clusters`, `cluster_subscriptions`, `cluster_invites`       |
| Purser           | Per-cluster pricing configuration                          | `billing_tiers` pricing rules                                |
| Bridge (Gateway) | GraphQL resolvers, union-type error handling               | Proxies to Quartermaster                                     |
| SvelteKit UI     | Marketplace browse, connect, request access                | `website_application/src/routes/infrastructure/marketplace/` |

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
- `updateClusterMarketplace(clusterId, input)` — operator updates visibility, pricing, approval settings
- `setPreferredCluster(clusterId)` — tenant sets default cluster for new streams

## Key Files

- `pkg/graphql/schema.graphql` — `MarketplaceCluster`, `ClusterSubscription`, `ClusterVisibility`, `ClusterPricingModel`, `ClusterSubscriptionStatus` types
- `api_gateway/internal/resolvers/infrastructure.go` — resolver implementations
- `api_tenants/internal/grpc/server.go` — Quartermaster RPC handlers (`RequestClusterSubscription`, `ApproveClusterSubscription`, etc.)
- `pkg/proto/quartermaster.proto` — `ListMarketplaceClusters`, `RequestClusterSubscription`, `ApproveClusterSubscription`, `RejectClusterSubscription` RPCs
- `pkg/database/sql/schema/quartermaster.sql` — `cluster_subscriptions`, `cluster_invites` tables
- `website_application/src/routes/infrastructure/marketplace/+page.svelte` — marketplace UI

## Future Work

- Cluster SLA enforcement across operators
- Minimum compliance bar for third-party clusters
- Operator onboarding documentation / self-serve flow

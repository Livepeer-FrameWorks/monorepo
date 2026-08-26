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
  │ subscribeToCluster             │ CreateClusterSubscription                         │
  │───────────────────────────────→│─────────────────────────────────────────────────→│
  │                                │                        │← proof-bound active or   │
  │                                │                        │  pending materialization │
  │← checkout / pending approval / active                                              │
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

| Service          | Role                                                           | Data                                                                  |
| ---------------- | -------------------------------------------------------------- | --------------------------------------------------------------------- |
| Quartermaster    | Cluster discovery and proof-bound access-state lifecycle       | `infrastructure_clusters`, `tenant_cluster_access`, `cluster_invites` |
| Purser           | Pricing, eligibility, payment, and commercial access decisions | `cluster_pricing`, tenant billing tiers, payment subscriptions        |
| Bridge (Gateway) | GraphQL resolvers, union-type error handling, pricing merge    | Proxies to Quartermaster and Purser                                   |
| SvelteKit UI     | Marketplace browse, connect, request access                    | `website_application/src/routes/infrastructure/marketplace/`          |

## Data Model

### Cluster visibility

- `PUBLIC` — listed in marketplace, discoverable by all tenants
- `UNLISTED` — omitted from listings but accessible by direct cluster link; access is purchased through Purser
- `PRIVATE` — invite-only, not listed

### Pricing models

- `FREE_UNMETERED` — no charge
- `METERED` — usage-based billing
- `MONTHLY` — fixed monthly subscription (price in cents)
- `TIER_INHERIT` — follows tenant's billing tier
- `CUSTOM` — operator-defined

### Subscription lifecycle

```
Tenant starts access through Purser
  │
  ├─ MONTHLY → PENDING_PAYMENT → verified webhook → ACTIVE
  ├─ requiresApproval / CUSTOM → PENDING_APPROVAL (is_active=false)
  │                                │
  │                                ├─ Operator approves → ACTIVE
  │                                └─ Operator rejects  → REJECTED
  └─ eligible FREE / METERED / TIER_INHERIT → ACTIVE
```

`MONTHLY` plus `requiresApproval=true` is rejected in v0.3 because activating
it safely requires a two-proof payment-and-approval state machine. It must not
silently accept either proof as a substitute for the other.

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

- `subscribeToCluster(clusterId)` — routes public marketplace access through Purser and returns active, pending approval, or checkout state
- `requestClusterSubscription(clusterId, inviteToken?)` — invite/private path only; direct commercial requests fail closed
- `approveClusterSubscription(subscriptionId)` — operator approves
- `rejectClusterSubscription(subscriptionId, reason?)` — operator rejects
- `updateClusterMarketplace(clusterId, input)` — operator updates visibility/approval in Quartermaster and pricing fields in Purser
- `createClusterInvite(input)` / `revokeClusterInvite(inviteId)` / `acceptClusterInvite(inviteToken)` — invite-based access
- `setPreferredCluster(clusterId)` — tenant sets default cluster for new streams

Invites are an authority source only for clusters classified as
`tenant_private`. Marketplace visibility (`PUBLIC`, `UNLISTED`, or `PRIVATE`)
does not alter that boundary: third-party marketplace access always enters
through Purser, so an invite cannot skip pricing, operator eligibility,
approval, or payment. When preferred access is revoked, unsubscribed, or no
longer routable, new work falls back to the tenant's still-entitled
platform-official cluster; passive expiry uses the same routing fallback.

## Key Files

- `pkg/graphql` — `MarketplaceCluster`, `ClusterSubscription`, `ClusterVisibility`, `ClusterPricingModel`, `ClusterSubscriptionStatus` types
- `api_gateway/internal/resolvers` — resolver implementations
- `api_tenants/internal/grpc` — Quartermaster proof-bound materialization and owner approval handlers
- `api_billing/internal/grpc` — Purser pricing, eligibility, checkout, and commercial subscription handlers
- `pkg/proto` — `ListMarketplaceClusters`, `RequestClusterSubscription`, `ApproveClusterSubscription`, `RejectClusterSubscription`, `SetClusterPricing` RPCs
- `pkg/database/sql/schema` — `quartermaster.tenant_cluster_access`, `quartermaster.cluster_invites`, `purser.cluster_pricing` tables
- `website_application/src/routes/infrastructure/marketplace` — marketplace UI

## Future Work

- Cluster SLA enforcement across operators
- Minimum compliance bar for third-party clusters
- Operator onboarding documentation / self-serve flow

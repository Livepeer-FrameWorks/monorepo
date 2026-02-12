# Routing Events: Tenant + Cluster Attribution

- **Last updated:** 2025-12-18
- **Scope:** Routing decision telemetry (viewer endpoint resolution) emitted by Foghorn and queried via Periscope/Bridge.

## Summary

“Routing events” represent a _single routing decision_ made by Foghorn when resolving viewer playback endpoints (HTTP or gRPC).
They are persisted in ClickHouse (`periscope.routing_decisions`) and exposed through Periscope-Query and Bridge GraphQL.

This document explains the **current** attribution model:

- `tenant_id` = **infra owner tenant** (cluster operator; dataset ownership / visibility boundary)
- `stream_tenant_id` = **subject tenant** (stream/customer owner; relevance filter)
- `cluster_id` = **emitting cluster** (Quartermaster cluster identifier; slicing/debugging)
- `remote_cluster_id` = **remote cluster** (set when viewer was routed cross-cluster via origin-pull or redirect; empty for local-only decisions)

## Data model

### Event payload (Foghorn → Decklog)

Routing decisions are sent as `LoadBalancingData` inside a `MistTrigger` envelope:

- `pkg/proto/ipc.proto` (`message LoadBalancingData`)
- `pkg/clients/decklog/client.go` (`SendLoadBalancing` wraps into `MistTrigger_LoadBalancingData`)

Key fields:

- `tenant_id`: infra owner tenant (cluster operator)
- `stream_tenant_id`: subject tenant (stream/customer)
- `cluster_id`: emitting cluster id (string)
- `internal_name`: canonical internal stream identifier (no `live+`/`vod+` prefix)
- `selected_node`, `selected_node_id`, `score`, `status`, `details`
- `client_bucket`, `node_bucket`: privacy-preserving location buckets

Privacy note:

- Raw client IP is redacted before emission (`client_ip` is empty); location is bucketized.

### Storage (ClickHouse)

Routing events are stored in:

- `pkg/database/sql/clickhouse/periscope.sql` (`CREATE TABLE routing_decisions`)

Columns include:

- `tenant_id UUID` (infra owner tenant)
- `stream_tenant_id Nullable(UUID)` (subject tenant)
- `cluster_id LowCardinality(String)` (emitting cluster)
- `remote_cluster_id LowCardinality(String)` (remote cluster for cross-cluster decisions; empty for local)
- `internal_name String` (stream identifier)

## Emission paths (Foghorn)

Foghorn emits routing events for **both** viewer-resolve paths.

### 1) HTTP `/play/*` and `/resolve/*`

Generic viewer playback endpoints:

- `api_balancing/cmd/foghorn/main.go` (routes `/play/*path` + `/resolve/*path`)
- `api_balancing/internal/handlers/handlers.go` (`HandleGenericViewerPlayback`)

Emission occurs after successful resolution:

- `api_balancing/internal/handlers/handlers.go` (`emitViewerRoutingEvent`)

### 2) gRPC `ResolveViewerEndpoint`

gRPC viewer endpoint resolution:

- `api_balancing/internal/grpc/server.go` (`ResolveViewerEndpoint`)

Emission occurs after successful resolution:

- `api_balancing/internal/grpc/server.go` (`(*FoghornGRPCServer).emitRoutingEvent`)

## How attribution values are determined

### `cluster_id` (emitting cluster)

Foghorn reads `CLUSTER_ID` from environment and also caches the cluster id returned by Quartermaster during bootstrap:

- `api_balancing/internal/handlers/handlers.go` (`Init` bootstraps service and caches `clusterID`)

Quartermaster bootstrap request supports explicit cluster binding:

- `pkg/proto/quartermaster.proto` (`BootstrapServiceRequest.cluster_id`)
- `api_tenants/internal/grpc/server.go` (`BootstrapService`)
  - If multiple active clusters exist, `cluster_id` is required.

### `tenant_id` (infra owner tenant)

Quartermaster returns the cluster owner tenant id in bootstrap response:

- `pkg/proto/quartermaster.proto` (`BootstrapServiceResponse.owner_tenant_id`)
- `api_tenants/internal/grpc/server.go` (`BootstrapService` populates it from `infrastructure_clusters.owner_tenant_id`)

Foghorn caches this value and uses it as the routing event `tenant_id`:

- `api_balancing/internal/handlers/handlers.go` (`ownerTenantID`)

### `stream_tenant_id` (subject tenant)

Foghorn resolves the subject tenant from Commodore during content resolution:

- `api_balancing/internal/control/playback.go` (`ResolveContent` returns `TenantId`)
- `api_balancing/internal/control/resolver.go` (`ResolveStream` / Commodore resolution)

This tenant is emitted as `LoadBalancingData.stream_tenant_id`.

## Ingestion and querying

### Decklog → Kafka

Decklog publishes routing events as Kafka analytics events with `event_type = "load_balancing"`:

- `api_firehose/internal/grpc/server.go` (`unwrapMistTrigger` + `convertProtobufToKafkaEvent`)

### Periscope-Ingest → ClickHouse

Periscope-Ingest writes routing events to ClickHouse:

- `api_analytics_ingest/internal/handlers/handlers.go` (`processLoadBalancing`)

### Periscope-Query → Bridge

Periscope-Query reads routing events and supports optional filters:

- `api_analytics_query/internal/grpc/server.go` (`GetRoutingEvents`)
  - provider-scope via `tenant_id` and `related_tenant_ids`
  - optional filters:
    - `stream_tenant_id`
    - `cluster_id`

Bridge GraphQL exposes routing events with the same filters:

- `pkg/graphql/schema.graphql` (`routingEventsConnection(... subjectTenantId, clusterId ...)`)
- `api_gateway/internal/resolvers/analytics_connections.go` (`DoGetRoutingEventsConnection`)

Subscription-based visibility (infra pool model):

- Bridge gathers provider owner tenant IDs from Quartermaster subscriptions and passes them as `related_tenant_ids`:
  - `api_gateway/internal/resolvers/analytics_connections.go`
  - `api_gateway/internal/resolvers/analytics.go` (`loadRoutingEvents`)

Typical usage:

- **Infra operator:** query with `subjectTenantId = null` to see cluster-wide routing events; optionally set `clusterId`.
- **Subscriber/customer:** query with `subjectTenantId = <my tenant id>` (and optionally `stream`) to see only their relevant routing events.

## Troubleshooting

- If routing events show `tenant_id = 00000000-0000-0000-0000-000000000000`, Foghorn likely emitted before caching `owner_tenant_id` or without cluster binding; check:
  - `CLUSTER_ID` is set in env (`config/env/base.env`, `docker-compose.yml`)
  - Quartermaster bootstrap response includes `owner_tenant_id`
  - Foghorn logs around bootstrap in `api_balancing/internal/handlers/handlers.go`

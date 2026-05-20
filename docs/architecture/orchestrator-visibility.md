# Orchestrator Visibility

Per-orchestrator discovery, state, and outcome telemetry from FrameWorks-managed Livepeer gateways. Source of truth for the federation map's orchestrator layer and its side-panel breakdown.

## Identity vs instance

A Livepeer orchestrator is identified by its **eth address**. That identity is load-balanced across **N backing instances** behind a single DNS hostname. Each instance is its own go-livepeer process with **independent** price, capabilities, hardware, advertised sub-nodes, and version. Instances are usually deployed with consistent config in practice but **not guaranteed** — divergence is legitimate observed state, not drift.

The data model preserves that distinction:

- `orchestrator_state_current` — keyed by `(cluster_owner_tenant_id, orch_addr)`. Identity-level only: orch_addr, last_seen, updated_at. **Does not** carry pricing/capabilities/hardware.
- `orchestrator_instance_state_current` — keyed by `(cluster_owner_tenant_id, orch_addr, resolved_ip)`. Per-instance facts: canonical_url, advertised_node_urls, capabilities, base price_per_unit/pixels_per_unit, typed capability/model price arrays, hardware, source.
- `orchestrator_vantage_current` — keyed by `(cluster_owner_tenant_id, gateway_id, orch_addr, resolved_ip)`. Per-(gateway, instance) observation: lat/lng/city/country/geo_source/geo_resolved_at, latest_latency_ms, score, dialed_recently, last_seen.
- `orchestrator_discovery_samples` — raw 30d, keyed by `(ts, gateway_id, gateway_region, orch_addr, resolved_ip)`. Multi-IP rows preserved (one per resolved IP per attempt).
- `orchestrator_discovery_5m` / `_1h` — rollups. **Only `dialed=1` rows count as attempts**; sibling-IP rows from a multi-A-record DNS response are observation context, not extra attempts.
- `orchestrator_transcode_outcomes` + `orchestrator_transcode_hourly` — per-segment transcode result/error, keyed by resolved instance IP.
- `orchestrator_ai_outcomes` — separate table keyed by resolved instance IP; pricing meters and consumers differ from transcode.

## Pipeline

```
go-livepeer gateway
  └── SendGatewayTelemetry RPC (DecklogService)
        └── api_firehose (Decklog) → Kafka analytics_events
              └── api_analytics_ingest (Periscope-Ingest) → ClickHouse
                    └── api_analytics_query (Periscope-Query) gRPC
                          └── api_gateway (GraphQL)
                                └── website_application federation map
```

## Tenant attribution

Cluster-scoped events (`orchestrator_discovery_observed`, `orchestrator_state_update`) stamp `tenant_id = cluster_owner_tenant_id` resolved at provisioning time from the gitops manifest's `cluster.owner_tenant` alias. Session-scoped events (transcode/AI outcomes) stamp `tenant_id = stream_tenant_id` propagated through the Foghorn auth response → go-livepeer `StreamParameters.TenantID`; `cluster_owner_tenant_id` rides as a separate column for dual-attribution joins (mirroring `purser.invoice_line_items.cluster_kind` + `cluster_owner_tenant_id`).

Decklog rejects every gateway telemetry event missing `cluster_owner_tenant_id`. It also rejects transcode/AI outcome events missing `stream_tenant_id`. Client-side validation errors return typed gRPC errors; DLQ remains reserved for downstream Kafka publish failures.

## Multi-IP observation

DNS round-robin / geo-anycast for an orchestrator hostname is a first-class dimension. Per discovery cycle, the gateway resolves the hostname and emits one `orchestrator_discovery_observed` row per resolved IP — `dialed=true` on the IP the gateway actually dialed (carries `discovery_latency_ms` + `reachable`), `dialed=false` on sibling A-record IPs (geo-only context). Federation map renders one pin per `(gateway, resolved_ip)` so multi-IP orchs are visible.

## Geo attachment

The gateway uses `pkg/geoip` (same library foghorn/quartermaster use) to attach lat/lng/city/country at the gateway's network vantage. Routing telemetry through Foghorn just for geo would lose the gateway's perspective — different gateways may resolve the same hostname to different IPs with different geo, and that's the signal we want recorded.

`GEOIP_MMDB_PATH` is provisioned to `livepeer-gateway` alongside `foghorn` and `quartermaster` via `cluster sync-geoip` and `cluster_provision.go`.

## Provisioning env

Injected per-gateway from gitops cluster facts:

- `FRAMEWORKS_CLUSTER_ID`
- `FRAMEWORKS_CLUSTER_OWNER_TENANT_ID` (alias resolved to UUID via QM at provision time)
- `FRAMEWORKS_GATEWAY_ID`
- `FRAMEWORKS_GATEWAY_REGION`
- `FRAMEWORKS_DECKLOG_GRPC_ADDR`
- `FRAMEWORKS_DECKLOG_TLS_MODE`
- `GRPC_TLS_CA_PATH` and `DECKLOG_GRPC_TLS_SERVER_NAME=decklog.internal`
- `SERVICE_TOKEN` for Decklog auth; `FRAMEWORKS_DECKLOG_AUTH_TOKEN` may override it for a distinct gateway token.
- `GEOIP_MMDB_PATH`

# Livepeer observability and notification routing

## Status

Draft, 2026-10-02. This is a planning proposal based on the current FrameWorks and sibling go-livepeer checkouts. Deployment state has not been verified. Product selections are recorded below; unselected constants and measurement details remain explicit implementation proposals. The 2026-10-04 batch responses supersede earlier menus.

Inspected revisions: FrameWorks `f91da1839`, go-livepeer `b9cde332`. Both checkouts were clean before this planning work.

## Summary

Build public Livepeer network insight and platform debugging from attributable gateway and regional prober observations: discovery, reachability, production performance, prices, errors, selection and stream impact. Consolidate the operational hodler/prober roles into one generic monorepo Go module with central and regional binaries, retaining existing analytics/API owners. Transcoding is the initial workload; future AI, Edge and external compute/storage providers remain outside the initial implementation.

Extend Lookout's existing notification destinations so ops and business pulse can use separate Slack/Discord channels. Retain a generic network purpose for future routes, but implement no Livepeer notification or automatic network-incident system initially. Existing ops incidents remain available for actual platform/customer impact; poor public network measurements alone do not create an incident.

## Confirmed product direction

The initial audiences are platform operators debugging delivery and the general public understanding and comparing Livepeer compute. Public Livepeer insight requires neither a FrameWorks account nor wallet ownership proof. Livepeer is a permissionless public network; publish network identity, endpoints, discovery, quotes and comparable production performance without orchestrator enrollment or consent gates. Customer stream/session identifiers, credentials and private managed-resource data remain separately scoped operational data.

Use the single production Livepeer network used by the gateways. Testnets are outside initial scope. Staging measurements stay in the staging environment and never mix into production comparisons. Keep provider/network namespaces in the generic contracts without implementing extra networks or workloads now.

Deliver a dedicated `frameworks.network/network` page alongside corresponding application/operator views and permitted API reads. The referenced LivepeerPerformanceTranscoder site is an experience/source reference only; repointing, redirecting or migrating that dashboard and its consumers is outside this project.

FrameWorks owns catalog refresh, inventory union, observer assignments and gateway candidate publication. Reuse go-livepeer contract/RPC helpers where useful rather than inheriting its entire discovery loop. Gateways contribute discovery and real transcode results independently, retain local caches/discovery, and validate shared candidates against existing trusted/untrusted pools, capability/payment/price requirements, scores and suspensions. Roll out shared candidates in shadow mode before supplementation. An empty, stale, unusable or unavailable feed must not block local operation or erase a usable pool. Bounded gateway background refresh accelerates before usable capacity is exhausted.

Each prober resolves the complete hostname catalog every five minutes, including multiple probers in one region. Separate DNS enumeration from directly targeted discovery RPCs. Accumulate endpoints across observers, regions and time; a region is not an instance uniqueness boundary. Attribute checks to the actual remote socket using the original authority. DNS disappearance alone is not downtime.

Routine targeted RPCs use up to two healthy probers under the provisional relative-distance rule: choose the nearest, and include the second only if `d2 <= 2 * d1`. This avoids forcing a distant second when a close primary exists. Where all observers are far away, still use the best available and expose where the measurement came from. Geography-only assignment remains the initial policy for located endpoints. Unknown locations remain visible; a healthy fallback observer and explicitly labelled path evidence can provide coverage without inventing coordinates.

Any fresh attributable discovery success from a healthy actual gateway or prober establishes observed endpoint availability for that interval. With no fresh success, complete qualifying failures from every assigned healthy observer establish unavailable from the checked locations, including one assigned observer. Missing, unhealthy or ambiguous coverage is unknown. Preserve region/path results separately; a bad US path does not make an endpoint bad from Europe.

Normal RPC cadence is 60 seconds. Back off after qualifying failures, with a proposed doubling schedule capped at 15 minutes; fresh success restores normal cadence. Active on-chain orchestrators and still-advertised/discoverable endpoints never leave monitoring merely because they have failed for 30 days. Retire abandoned endpoint detail after the agreed 30-day recovery window only when no current reference keeps it monitored. Hide unsuccessful endpoints from the default map after seven days, with Show inactive; display policy does not control monitoring.

Production transcodes supply performance measurements, with staging traffic in staging. DNS/discovery RPCs continue as agreed, but no synthetic transcode jobs or paid benchmark scheduler are initial scope. Compare region/workload cohorts, sample counts, tail latency, deadline misses and consistency rather than a universal global score. A single fast result cannot establish suitability for a long-running stream.

Detailed discovery, workload, state and price evidence retain 30 days; no separate extended price-history window is required. Existing finite rollups remain unless changed explicitly. Retention of historical evidence is independent of the current catalog/minimal monitoring state needed to continue testing active resources.

External probers may run on non-FrameWorks hosts, provisioned manually rather than through the CLI. Start with a shared ingest secret over verified TLS and a narrow validated observer API, following the simplicity of the current fleet. Per-prober credentials, certificate enrollment and a provisioning system are deferred. Managed/self-hosted workers already connected to Foghorn retain that observation path.

Distinguish dashboard observations from ops incidents. The dashboard includes unreachable, slow, expensive, incompatible, suspended and unused orchestrators with regional evidence. Ops incident eligibility depends on effect on our gateways/streams: late or missing output, budget exhaustion, interruption, usable-pool exhaustion or sustained fallback pressure. A successful seamless failover is a measurement, not automatically an incident. Initial scope excludes standalone Livepeer notifications, external orchestrator outreach and new automatic escalation rules.

## Network observation responsibilities and hodler integration

Separate how a resource is known, how it is observed, what jobs it supports, and where an actionable finding is delivered. On-chain versus off-chain discovery is independent of workload type. Managed connection liveness, externally observed endpoint reachability, available capacity, and successful job execution are distinct facts, with observer provenance and regional paths retained.

| Responsibility                                                                                  | Proposed owner or source                                                  | Integration boundary                                                                                                                          |
| ----------------------------------------------------------------------------------------------- | ------------------------------------------------------------------------- | --------------------------------------------------------------------------------------------------------------------------------------------- |
| Connected self-hosted/managed worker identity, registration, capabilities and liveness          | Existing Quartermaster ownership/registry and Foghorn control connections | Extend existing observation exports; do not route every managed worker through the on-chain prober system                                     |
| External on-chain orchestrator catalog                                                          | Contract adapter using BondingManager and ServiceRegistry                 | Supplies external identities/URIs; does not imply platform-managed ownership                                                                  |
| External endpoint DNS and targeted RPC observations                                             | Regional probers and actual gateways                                      | Full-catalog DNS per prober, locality-limited targeted tests, attributable results                                                            |
| Catalog union, prober assignments and gateway candidate publication                             | Selected monorepo network-observation module, central binary              | Generic responsibility with a Livepeer adapter initially; protocol-specific candidate output                                                  |
| Measurement history, price history, regional performance, coverage, public/operator projections | Decklog/Kafka and Periscope                                               | Reuse the existing pipeline and authorized reads; no duplicate hodler analytics database                                                      |
| Public network API, website/webapp dashboards                                                   | Bridge and existing network/map surfaces                                  | New dedicated public Network page and shared permitted read model; reference dashboard is out of scope                                        |
| Actual platform/customer impact incidents, acknowledgement/assignment and investigation         | Lookout                                                                   | Reuse existing ops handling; routine public network observations are dashboard evidence, with Livepeer-specific incident automation deferred  |
| Chat/email destinations and retries                                                             | Lookout routing/outboxes                                                  | Purpose and destination independent of observation source/workload; in-app incident existence does not depend on successful external delivery |

The current hodler's roles distribute across these owners. The maintainer selected monorepo-owned Go observation/prober code in one new generic module so contracts, deployment, and protocol helpers evolve together. Foghorn remains the controller for its connected workers; Lookout remains the incident/delivery owner. Module/binary names, detailed migration gates and deployment configuration are still proposals, and no runtime implementation is authorized by these planning selections.

### Selected consolidation, proposed implementation details

Use one new monorepo Go module (selected option A), provisionally `api_network_observer`, with two binaries:

```text
api_network_observer/
  cmd/network-observer/       central inventory, assignments and candidate API
  cmd/network-prober/         regional DNS and assigned endpoint RPC tests
  internal/providers/livepeer/  contract catalog and Livepeer protocol helpers
```

These are proposed paths and names, not existing files. Deploy the central binary with the platform services, initially one active scheduler. Deploy the regional binary on the current prober hosts/regions; it does not need to run on every gateway or managed worker. The two binaries share typed contracts/helpers in one module. The Livepeer provider code is ordinary code that reads BondingManager membership and ServiceRegistry URIs and speaks the existing discovery protocol; it is not a separate deployed adapter service or an embedded gateway process.

The central binary replaces the operational part of the Node hodler: refresh the external catalog, merge observed endpoints, track configured probers and their health/location, assign up to two eligible RPC testers, validate/accept results, and publish authenticated versioned gateway candidate snapshots. It owns a service-scoped PostgreSQL schema for catalog/inventory, assignments, observer registration, and durable delivery state. Historical time-series and comparisons remain Periscope-owned. Replica coordination/leadership is required before adding active scheduler replicas; do not introduce multiple independent schedulers initially.

Each regional binary fetches the shared catalog and its test assignments, resolves every catalog hostname periodically, directly tests assigned endpoints using the original authority, and submits attributable results under its registered observer identity. It retries/spools safely under the agreed telemetry contract. The central API accepts the shared observer ingest secret over verified TLS, validates configured observer labels/assignments and result structure, and forwards normalized probe observations through Decklog/Kafka with stable event IDs and durable acknowledgment. Shared-secret authentication establishes membership in the trusted fleet, not cryptographic identity of an individual prober; configured labels provide provenance within that trust model. Tenant/cluster scope comes from server configuration. Probers get a narrow API, not unrestricted Decklog credentials.

Existing gateways continue emitting discovery and actual workload results directly through Decklog/Kafka. The central binary consumes the relevant normalized discovery/state events to update its endpoint inventory, independently of Periscope history queries. Gateways fetch its candidate feed while retaining the agreed local discovery, selection, caches and fallback. Existing connected managed workers remain on their Foghorn path; their observation exports feed shared analytics without becoming external on-chain probe jobs. This keeps external catalog/prober coordination independent of media request handling, historical queries and notification delivery.

Proposed deployment sequence: build the central module and Go prober binary, validate ingestion/attribution against controlled fixtures, and replace current prober/hodler duties gradually under bounded checking budgets. New binaries run on manually managed prober hosts where appropriate; no CLI enrollment dependency. Compatibility with an old prober transport is optional implementation plumbing, not a requirement to preserve its public feed, external leaderboard worker or website. Do not copy legacy leaderboard results or manufacture old uptime. External repository/dashboard modification is outside this planning scope.

The single generic module with central/regional binaries is selected. Names and rollout mechanics are implementation proposals. Periscope, Bridge, Foghorn and Lookout retain the responsibilities above.

### Generic core and provider boundaries

The core owns resource inventory, observer registration, assignments, observation provenance/freshness, durable delivery and authenticated feeds. Provider identity and network identity are explicit namespaces; resource IDs are provider-defined and do not universally require a chain ID, Ethereum address, hostname or IP. Endpoints carry a protocol/address representation with optional socket and geo facts. Provider-specific identities/payloads are typed at the adapter boundary; do not implement every resource as a Livepeer orchestrator with renamed fields.

Separate capability category (e.g. compute or storage), actual workload/check kind, and measurement source. Preserve units and measurement semantics per kind: storage observations must not need transcode pixels/RTR, and compute observations must not inherit storage pricing/throughput units. Common reachability/timing facts can be shared where their definitions match; provider/workload-specific metrics and exact quote units remain explicit. A missing/unsupported measurement is unknown or not applicable, rather than a fabricated zero. Public views and findings consume those declared semantics rather than a universal cross-network ranking score.

Use small compile-time Go provider interfaces/helpers for catalog acquisition, endpoint resolution where supported, protocol checks, result normalization and consumer feed mapping. Livepeer is the only external provider implemented initially. Its contract pool reads, DNS enumeration, discovery RPCs, ticket/price/capability facts, and go-livepeer candidate format stay in its provider code. Full-catalog DNS and up-to-two nearby RPC checks are selected Livepeer policies, not mandatory discovery/test rules for every future network. Future external storage/compute provider code may use another registry, address format or check protocol. Each provider declares supported checks and eligible assignment policy without gaining unrestricted queue/notification access.

Keep the first version to ordinary interfaces, typed observations and explicit routing, with no dynamic plugin loader, arbitrary remote code execution, universal benchmark framework or speculative storage adapter. Adding Ethereum Swarm or another external network later means implementing its provider discovery/check/metric/feed semantics and reviewing its public fields and job budgets, while reusing inventory, observers, history, map and Lookout delivery. Managed self-hosted observations continue entering through Foghorn and retain their existing ownership/privacy boundaries.

Keep the first implementation focused on transcoding and the agreed DNS/RPC jobs. Preserve resource identity, discovery origin, observation source, workload/capability and pricing-meter dimensions so AI adapters and Edge job observations can be added later without duplicating the whole monitoring stack. Do not implement a universal plugin/job framework in this first slice. Missing or disconnected Foghorn control sessions require appropriate connection/observer diagnostics; they do not automatically justify applying on-chain RPC tests or establish that all work on the node is down. Additional managed-worker hardware/capacity fields are optional extensions, not facts assumed to be present today. Private self-hosted resources do not become public merely by entering the shared observation model.

```text
gateways + Go observer/probers → Decklog → Kafka → Periscope facts/history
                                                     │
                                            Bridge → public/operator views
source service activity / impact-based ops incidents → Lookout
                                                         │
                                              configured destinations
                                                ├→ ops
                                                └→ business pulse
```

## Current state

| Area              | Existing behavior                                                                                          | Extension needed                                                                          |
| ----------------- | ---------------------------------------------------------------------------------------------------------- | ----------------------------------------------------------------------------------------- |
| Lookout delivery  | Incident and operator activity outboxes, retries, leases, retention, email/Slack/Discord adapters          | Purpose-specific destinations and persisted routing identity                              |
| Business pulse    | Source-checked activity allowlist includes signups, subscriptions, payments, support, and cluster requests | Separate business traffic from operations; keep the source checks                         |
| Gateway discovery | Both regular pool and DB-cache polling emit RPC observations, including failures and DNS sibling context   | Record cycle coverage, actual compatibility decision, sampling source, and dial certainty |
| Gateway state     | Prices, capabilities, advertised nodes, coarse hardware summary                                            | Historical snapshots, structured fields, freshness, optional version/capacity evidence    |
| Gateway outcomes  | Per-attempt transcode and AI success/failure, timings, instance IP, and stream tenant                      | Stable error taxonomy, timing presence, retry correlation, workload dimensions            |
| Decklog           | Validates owner/stream attribution and publishes to `analytics_events`                                     | Preserve producer identity and envelope dimensions through replay                         |
| Periscope storage | Current identity/instance/vantage tables; raw discovery/outcomes and rollups                               | State/price history, replay-safe aggregates, coverage and findings queries                |
| Periscope API     | 5-minute/hourly discovery and workload counts, means, maxima                                               | Price history, errors, percentiles, coverage, health timeline, operator aggregate         |
| Web application   | Federation map with orchestrator pins, instance details, and performance summary                           | Historical charts, filters, evidence panels, explicit unknown/stale states                |

Raw discovery, transcode, and AI records currently expire after 30 days. Discovery 5-minute rollups retain 180 days, discovery hourly rollups retain one year, and workload hourly rollups retain two years. Instance state is a current `ReplicatedReplacingMergeTree` snapshot, not a retained price timeline. The inspected baseline has no TTL clause on orchestrator identity, instance or vantage current-state tables; raw/rollup TTLs do not automatically expire those snapshots or the proposed PostgreSQL observer inventory. Deployment retention has not been verified.

Two current details affect the design:

1. `SendGatewayTelemetry` calls Decklog synchronously despite the client being named `BatchedClient`. The gateway wrapper logs failure without durable replay. Decklog generates a new analytics event ID, and the current orchestrator raw tables/additive materialized views do not demonstrate event-ID deduplication. A trustworthy historical record needs an explicit completeness and replay contract.
2. Discovery/state rows belong to the cluster owner, while workload rows belong to the stream tenant. GraphQL derives network scope from owners of public and accessible gateway clusters, then queries performance under those owner IDs. This can omit workloads belonging to other tenants on the same gateway. It does not justify widening public access to those tenants' raw rows.

The gateway currently emits `compatible=true` before downstream compatibility checks. Its `hardware` summary contains pipeline/model names rather than GPU inventory. Upstream `GPUComputeInfo` has GPU identity/name, compute capability, and memory fields, but the inspected contract has no driver version or driver patch state. Capability price entries may carry an array position without a semantic capability/model name.

## Goals

- Record what each gateway actually observed about each orchestrator endpoint over time.
- Show discovery latency, workload latency, error frequency, prices, and coverage with useful filters and clear units.
- Distinguish an unavailable endpoint, insufficient capacity, incompatibility, missing observations, and a broken telemetry path.
- Split existing operational and business destinations, with a generic purpose/destination model that can later support Livepeer messages.
- Explain network/discovery/selection behavior and correlate actual platform delivery impact; retain existing impact-based ops incident handling.
- Preserve extensible observation/provider contracts without requiring future contacts, outreach or synthetic workloads now.

## Initial boundaries

Initial delivery includes full diagnostics for platform operators and public Livepeer insight for anyone. Public network metrics require no account, wallet proof or orchestrator opt-in. Tenant workload views remain limited to that tenant's facts, and private stream/credential/managed-resource details stay scoped.

The initial telemetry rollout preserves gateway selection, followed by the selected shadow/supplement candidate-feed integration. Observations cover the pools and regions that FrameWorks gateways and probers can see, with explicit gaps rather than a claim of complete network visibility. Historical diagnostics remain separate from authoritative processing/billing facts. External Livepeer operators need no FrameWorks account, fork or optional health extension to appear in public comparisons.

## Notification routing

### Separate purpose from transport

The maintainer selected `ops`, `business_pulse`, and `network` as message purposes (option A). Network routes can filter by provider/workload, such as `provider=livepeer` and `workload=transcode`, to choose a dedicated Livepeer destination. Treat Slack, Discord, and email as delivery transports. A destination has a stable ID, transport, enabled state, and credential/reference. Routes map a purpose, optional provider/workload filters and optional severity to one or more destination IDs. Purpose does not mean authorization scope: a tenant incident still cannot reach platform chat just because its purpose is `ops`.

| Message                                                                      | Purpose                                           | Proposed initial delivery                                                |
| ---------------------------------------------------------------------------- | ------------------------------------------------- | ------------------------------------------------------------------------ |
| Platform incident opened/recovered                                           | `ops`                                             | Configured ops chat; retain critical-only operator email policy          |
| Cluster subscription request or infrastructure approval                      | `ops`                                             | Ops chat                                                                 |
| Signup, stream created, subscription/payment/top-up event, newsletter signup | `business_pulse`                                  | Business chat                                                            |
| Support conversation/contact activity                                        | `business_pulse` initially                        | Business chat, with a future support purpose if requested                |
| Orchestrator reachability/latency/error observation                          | Future `network`, provider `livepeer`             | Dashboard evidence initially; no Livepeer notification                   |
| Suspected session-limit or capacity issue                                    | Future `network`, provider/workload from evidence | Classified dashboard symptoms; no initial outreach or automatic incident |

A customer payment failure remains a business event. A systemic payment-service outage remains an ops incident. Route existing activity definitions explicitly by event type rather than guessing from free-text categories. Future purposes should be additive to the same registry.

### In-app handling and future network purposes

Keep the existing Lookout incident lifecycle for actionable platform/tenant impact: evidence, affected path, severity, acknowledgement, assignment, recovery and investigation. External delivery remains separate from in-app availability. Business pulse remains activity rather than an incident. Public network status, errors and price changes do not enter Lookout solely because they belong on the dashboard.

The selected taxonomy is `ops`, `business_pulse`, and future `network`, with optional provider/workload filters. Implement the ops/business split initially. No Livepeer Slack/Discord/email delivery, contact records, standalone network findings engine or automatic escalation is initial scope. A later network route can reuse the destination model without restructuring the ops/business split. Existing platform/tenant authorization remains independent of purpose.

### First release configuration

Reuse Lookout's reloadable typed/env configuration with purpose-specific Slack and Discord URLs, for example `LOOKOUT_OPS_SLACK_WEBHOOK_URL`, `LOOKOUT_BUSINESS_PULSE_SLACK_WEBHOOK_URL` and their Discord equivalents. Existing critical ops email policy can remain on its current recipients; do not introduce business or Livepeer email subscriptions now. Operators create channels and corresponding webhooks manually. No destination administration UI, bot framework, new notification service or automated channel provisioning is required.

The current source has one `SlackWebhookURL` and one `DiscordWebhookURL` shared across incident/activity dispatch. Existing webhook payload builders and HTTP senders can be reused. Slack can create multiple channel-specific webhooks under the same app; a webhook cannot override its configured channel. Discord incoming webhooks also target channels. Thus separate destinations do not require separate Slack apps or a Discord bot. See [Slack's incoming webhook setup](https://docs.slack.dev/messaging/sending-messages-using-incoming-webhooks/) and [Discord's webhook reference](https://docs.discord.com/developers/resources/webhook).

Internally map configured destinations to stable IDs such as `ops.slack.primary` and `business_pulse.discord.primary`. Persist purpose, destination ID and transport when enqueuing; retries use that destination rather than today's default purpose route. Resolve secrets at send time and retain URL redaction. Credential rotation for the same destination is supported; replacing a destination with pending rows requires an explicit drain/cancel/reroute procedure.

Keep the two existing incident/activity outboxes, lease fencing, bounded retries, source checks and idempotency. Include destination identity in uniqueness so independent same-transport targets do not conflict. Ops activity such as cluster requests goes to ops; signup/payment/subscription/support activity goes to business pulse according to explicit event-type mapping. A disabled route does not fall back into the other purpose. This is a bounded extension of current delivery code, with real configuration, schema/compatibility and regression work; no implementation effort estimate is yet verified.

### Compatibility and migration

For existing events only, purpose-specific settings take precedence. While an explicit legacy compatibility flag is enabled, absent ops/business settings may use the existing shared webhook settings; report this in startup diagnostics so operators can see that traffic is still mixed. Explicitly disabled routes never fall back. No Livepeer route is enabled or enqueued in the first implementation; any future route must be explicit and cannot inherit the shared webhook automatically.

Backfill pending incident deliveries as ops, and pending activity deliveries using their stored event type. Preserve their legacy destination identity until drained; do not retarget already queued rows during upgrade. Expand schema and deploy backward-compatible readers before enabling new writes, then retire the legacy mapping after operators configure channels. Test mixed old/new binaries before declaring rollback safe. A user-facing UI is optional after the env-based split works.

## Telemetry contracts

### Identity and attribution

Preserve orchestrator identity and observed endpoint separately. The proposed identity is `(chain_id, orch_addr)`; the observed path also includes cluster, gateway, canonical endpoint, and actual remote IP. Existing IP-based instance rows are a useful observation key, but a shared IP can front several processes and an IP can change. Describe them as observed endpoints unless an authenticated instance identifier is advertised. Never treat DNS sibling IPs as successfully probed instances.

For first-seen failures, preserve the existing URL-based identity and add time-bounded URL/address associations when a later response establishes the wallet. Keep the original observations intact. Do not rewrite old history to an address without evidence; addresses and endpoint ownership can change.

Keep tenant IDs in Kafka headers according to the platform contract. Existing gateway RPC attribution fields still carry owner/stream context; they are not authorization grants. Decklog must validate allowed gateway/cluster ownership against authenticated provisioning context rather than trusting claimed IDs. All query APIs derive authorized scope before selecting data and every DB query filters by tenant.

### Proposed event extensions

| Event or fact                 | Fields to add or preserve                                                                                                                                          | Purpose                                                                                                                                               |
| ----------------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------ | ----------------------------------------------------------------------------------------------------------------------------------------------------- |
| All telemetry                 | Stable producer event ID, observed/received times, chain ID, producer/schema version, gateway boot ID                                                              | Replay, clock diagnostics, attribution, compatibility                                                                                                 |
| Discovery observation         | Attempt/cycle ID, source (`scheduled`/`session`), actual dial status, RPC success, service usability, compatibility result/reason, stage/error class               | Separate reachability from selection eligibility and capacity                                                                                         |
| Discovery cycle summary       | Expected pool/endpoints, attempted/completed/skipped counts, start/end, deadline, cadence, pool-refresh status                                                     | Sampling coverage and disappearance detection                                                                                                         |
| Gateway heartbeat             | Process health, last cycle, queue/spool age, drops, delivery failures                                                                                              | Detect observer or telemetry outage separately from orchestrator outage                                                                               |
| State snapshot                | Exact base/capability prices and units, typed capabilities/models, available hardware/capacity, version where advertised, presence flags                           | Price/capability history and diagnostic evidence                                                                                                      |
| Workload attempt              | Attempt ID, stable logical request key, retry ordinal, source duration, profile/codec/resolution or AI pipeline/model, timing presence and phase                   | Distinguish retries from customer-visible final outcomes                                                                                              |
| Workload error                | Bounded enum code/kind, failure stage, origin, sanitized detail                                                                                                    | Actionable trends without unbounded labels or credentials                                                                                             |
| Candidate decision            | Catalog/endpoint ID, gateway/region, discovery/eligibility/pool stage, bounded reason, evaluated/accepted/rejected/skipped disposition, rule version and freshness | Explain price/capability/payment exclusions, slow/failed discovery, suspensions and selection without inventing why unobserved candidates were unused |
| Final logical segment outcome | Private correlation key, source duration, overall elapsed time, retry/hedge count, selected endpoint changes, budget/deadline outcome and final usable output      | Separate failed attempts and seamless recovery from late/missing output                                                                               |
| Session/pool transition       | Affected endpoint(s), reason, selection/discovery/session-setup timing, usable headroom, suspension/recovery, transition ID                                        | Measure churn and time lost obtaining replacement capacity                                                                                            |
| Media delivery evidence       | Private stream/segment correlation, timestamp, late/missing rendition or MistServer delivery stall/error where available                                           | Establish platform/customer impact; gateway failover alone does not prove viewer interruption                                                         |

Keep optional fields genuinely optional. Zero milliseconds must not also mean "unmeasured"; missing price must not mean free. A capability price identified only by position remains position-labelled until its semantic identity is verified. Preserve the upstream price ratio exactly and use an explicit denomination/unit mapping before display; do not infer ETH versus wei from the GraphQL field name.

Measure discovery RPC duration separately from segment upload, remote processing, result download/verification, and complete gateway attempt duration. Audit the existing timing boundaries before assigning these labels. Add source media duration so processing speed can be compared across segment sizes. Report percentiles with workload dimensions and sample counts; keep success latency and time-to-failure distributions separate. Keep attempt success, retry/fallback frequency, and final request success as distinct metrics.

### Reliable delivery without delaying media

Move telemetry network calls off discovery and segment result paths. Use a bounded queue and local spool for observations/outcomes, reusing the repository's durable telemetry patterns where suitable. Keep the same producer ID across retries, authenticate each send, flush on shutdown with a deadline, and cap disk usage. On saturation, preserve a documented priority policy and expose explicit gaps/drops; do not make media availability depend on telemetry availability.

Decklog carries the producer ID into Kafka and only acknowledges a committed publish. A retry after an unknown RPC result must yield the same fact ID. Ingest must deduplicate before additive rollups: a `ReplacingMergeTree` on raw facts alone is insufficient because materialized views count every inserted block before replacement merges. Select and prove a durable deduplicating projection using the existing finalized-fact/ledger patterns before enabling replay. Record spool loss or gateway replacement as incomplete coverage.

Audit actual socket attribution. The current helper uses a host-level dial cache that can be reused by concurrent requests; stale or ambiguous IP attribution must become unknown rather than a confident instance assignment. Prefer connection/request-scoped remote address capture. DNS/TCP failures remain endpoint failures with no invented dialed-IP success.

## Historical storage and query APIs

Periscope owns ClickHouse facts, rollups, and read APIs. Add an append-only orchestrator state observation fact table while retaining current tables for map reads. Store each successful snapshot with freshness and semantic identifiers; derive price/capability changes by comparing valid observations. A missing snapshot during an outage does not create a zero-price observation. The first historical price begins when capture is deployed; old current state cannot reconstruct a historical timeline.

Extend discovery rollups with coverage, scheduled versus session samples, and classified failures. Add mergeable latency distributions and error aggregates by bounded class, endpoint, gateway region, and workload dimensions. Bound profile/model cardinality and query output. Retain detailed state and price observations/changes for 30 days, matching raw discovery/outcome retention. No separate longer price-change window is selected. Keep existing finite rollup windows; minimal monitoring state for still-active catalog resources follows catalog lifecycle rather than historical-detail TTL. Size storage before schema deployment. Do not imply existing long-term means can reconstruct percentiles or error detail that was never retained.

Introduce these bounded read APIs through Periscope gRPC, Bridge GraphQL, and the existing generated operation pipeline:

- State and exact price history with validity/freshness and endpoint filters.
- Error breakdown and a redacted, paginated sample list for authorized operators or the owning tenant.
- Performance series with sample counts, timing presence, workload filters, and p50/p95/p99 where enough observations exist.
- Availability/coverage series and discovery cycle timeline.
- Current/historical network status, selection reasons and impact evidence windows, without a separate initial network incident lifecycle.

Use explicit maximum windows, returned-point limits, pagination, and server-chosen rollup resolution. Cache keys include tenant scope, audience, chain, cluster, endpoint, gateway, workload filters, and interval.

### Operator aggregation and public scope

Publish insight for the permissionless Livepeer network to anyone: on-chain orchestrator identity, publicly advertised endpoint facts, capabilities/hardware when actually exposed, observed quotes, regional discovery/availability, production performance/error classes, sample counts and selection/exclusion summaries. No FrameWorks login, wallet proof, orchestrator opt-in or ownership claim is needed to browse it. An operator need not reuse an on-chain key in a browser. Upstream standard fields absent today remain unknown.

Raw workload facts remain stream-tenant scoped. Derive a separate sanitized network/operational projection with explicit provider/network/environment, gateway-region and workload dimensions; retain the server-assigned owner tenant for internal queries. Publish network metrics without customer/stream/session identity, media URLs, credentials or unfiltered errors. Public network insight is selected policy, not an opt-in permission per orchestrator. Internal detailed correlation and raw customer evidence still use authorized tenant/operator access. Private managed infrastructure does not become public because external Livepeer resources do.

Periscope owns projections and scopes; Bridge public reads use the explicit public projection rather than unrestricted cross-tenant raw reads. Query APIs enforce tenant and public-resource/environment filters. Quartermaster remains gateway ownership authority; Lookout uses clients rather than cross-service DB queries. A gateway-cluster owner's identity alone does not establish whether its managed topology is public. Production and staging results are isolated even for the same wallet or endpoint.

Publish measured candidate dispositions at safe aggregate granularity. A candidate not selected might be perfectly usable; report known policy reasons or 'not selected/no recorded decision' rather than labelling every idle orchestrator slow or faulty. Internal per-stream timing evidence can explain impact while public statistics omit stream identity.

## Gateway telemetry and candidate pool review

The maintainer requested a review of what gateway performance reaches the pipeline and whether the improved cached discovery/selection behavior is complete. The gateway is both a real-work observer and a consumer of eligible candidates. Probers complement its endpoint inventory and geographic coverage; they do not replace gateway observations.

### Verified source behavior

| Path                      | What exists in the inspected fork                                                                                                                                  | Limitation or review item                                                                                                                    |
| ------------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------ | -------------------------------------------------------------------------------------------------------------------------------------------- |
| Discovery/state analytics | Regular pool and DB polling emit RPC results and state to Decklog                                                                                                  | Telemetry must be configured; dial attribution, coverage, compatibility, and replay still need work                                          |
| Segment analytics         | `SubmitSegment` emits one outcome on return with success/error, overall/upload/body-phase timings, latency score, pixels, profiles, endpoint/IP, and tenant        | Missing stream tenant drops emission; source duration, workload identity, failure disposition, and reliable delivery are incomplete          |
| Regional Redis selection  | `orchperf:` stores workload/profile/endpoint EWMAs, sample/failure counts, last observation, attribution, and RTT mean/deviation; `orchhealth:` stores suspensions | This live scoring state is separate from analytics; its EWMA/queue/history semantics are not exported as an identical selection-state series |
| Discovery restart state   | Last good discovery snapshot is stored through the local database KV table; hydration validates chain/network/broadcaster/region and age                           | The inspected fork does not establish a Redis-backed discovery-set mirror; deployed configuration or other revisions remain unaudited        |
| Refresh recovery          | Async startup retries, empty-pool DB fallback, preserved capabilities on an all-failed refresh, and local snapshot hydration have focused tests                    | These protections do not establish per-instance inventory merging on a partly successful refresh                                             |

The existing Redis performance TTL is 48 hours and its in-process reader memo is 10 seconds; the discovery snapshot maximum age is 24 hours. These are current code defaults, not approved thresholds for the new dashboard or pool feed.

The segment telemetry defer runs before selection-specific exclusions. Redis skips hedge-loser cancellations and non-retryable input errors at that site, while the analytics event is still emitted. Add explicit outcome disposition so public failure rates and future score reconstruction can distinguish orchestrator faults, invalid input, local failures, and intentionally cancelled redundant work.

Timing labels also require review: current `uploadDur` spans sending the HTTP request through receiving response headers, and `transcodeDur` is the subsequent response-body read interval. Neither is proven to be pure upload or server compute time. Redis speed factors divide media duration by these gateway intervals; analytics lacks that media-duration field. Preserve raw timing boundaries and emit source duration before publishing RTR/processing-speed comparisons.

### Inventory and eligible pool

Maintain a durable observed inventory separately from each gateway's usable candidate set. A globally up instance may be unreachable from one gateway, incompatible with a requested profile, above its price limit, suspended, or missing usable payment/session facts. Public map visibility and pin aging must never implicitly determine serving eligibility.

Approved direction, with implementation details still proposed: FrameWorks supplies authenticated, versioned regional candidate snapshots from the shared inventory; gateways cache the last usable snapshot, validate candidates under their local policy, obtain fresh session/price/capability information, and report discoveries/outcomes back. Each regional snapshot may contain multiple endpoints belonging to the same orchestrator hostname. Keep bounded local refresh and exploration available when the platform feed is unavailable, according to an explicit fallback policy. Analytics/collector outages must not introduce a synchronous dependency into segment selection.

Snapshot contracts must bind chain, ownership/cluster scope, observer region, pool-policy version, generation, creation/expiry times, endpoint authority and identity, and evidence freshness. Publish updates atomically. Separate explicit policy removal from an incomplete discovery cycle. Redis can cache regional snapshots and fast selection state; durable inventory/history belongs to the owning platform service and Periscope projections. Choose the pool publisher, storage owner, API, and migration path after auditing current deployment.

Audit before changing serving behavior:

- A partly successful refresh currently replaces the in-memory capabilities list with the successful subset; review which previously known endpoints disappear and how selection recovers them.
- Confirm all-failed refresh, aged snapshot, webhook failure, startup, restart, Redis failure/failover, and platform-feed failure behavior together rather than validating each cache alone.
- Review endpoint versus resolved-IP identity, signer/service/payment identities, and Redis key isolation across region, chain, workload, and cluster policy.
- Compare Redis speed/EWMA semantics with exported outcomes, including queue drops, timing phases, early failures, hedging, and sparse-history exploration.
- Emit pool generation, discovery source, policy eligibility/rejection reason, snapshot age, and cache source so an operator can explain why an available instance was absent or unselected.

The shared-feed/local-validation option and shadow-before-supplement rollout are selected. Preserve existing hard eligibility policy; measure fallback budgets/headroom before selecting final engineering constants. This planning decision does not implement runtime behavior.

### Catalog source and discovery reuse

An on-chain catalog seeds orchestrator identities and registered service URIs; it does not enumerate the instances behind those URIs. In the inspected go-livepeer code, `eth.client.TranscoderPool()` walks BondingManager's pool using first/next entries, and `GetTranscoder()` reads each service URI through ServiceRegistry. Reading ServiceRegistry alone does not provide that pool enumeration. The existing DB discovery cache already uses this client path.

The maintainer selected a platform-owned catalog/scheduler using existing contract and RPC helpers where practical (option B): separate chain catalog refresh, regional DNS enumeration, targeted endpoint tests, and gateway candidate publication. Preserve known endpoints/history across catalog changes and record catalog freshness; on-chain membership and current observed reachability are separate facts. RPC/provider outages must not erase a last-known catalog. Exact chain scope, active/inactive coverage, refresh cadence, and failure handling await review.

### Gateway selection and fallback continuity

The gateway must retain a local discovery path when the shared feed is empty, unavailable, invalid, or has no candidates usable for the requested workload. This applies to cold starts without a platform snapshot and to running gateways that exhaust usable feed candidates. Preserve a usable local catalog/cache and permit bounded contract/DNS/RPC discovery independently of the FrameWorks coordinator. Continued transcoding still requires a reachable, eligible orchestrator and necessary network/payment dependencies; fallback does not waive those requirements.

Feed and local sources must enter the existing discovery/session-selection machinery, including capability/version checks, capacity, price and ticket validation, configured exclusions, trusted/untrusted pools, stake/performance selection, exploration, session reuse, and scoped suspensions. Discovery provenance does not confer trusted status. An empty feed must not set effective pool sizing/counts to zero while valid local candidates exist. Source merging must deduplicate candidates without collapsing different endpoints behind one hostname.

The inspected gateway creates separate trusted and untrusted `SessionPool`s. Its selection consumes local/session latency and durable endpoint performance where configured. Suspensions are region/workload/capability scoped through Redis when configured, or per-stream in memory without Redis. Redis operation failures need a separate resilience audit; lack of Redis configuration and a configured Redis outage are not equivalent. Discovery can return suspended candidates when healthy ones are insufficient, and session refresh currently admits them only when no fresh or existing sessions are available. Preserve and test this last-resort behavior during feed integration unless a separate reviewed policy changes it. Configured exclusions and invalid price/payment/capability checks remain enforced.

Feed updates and recovery must preserve active sessions and applicable suspension/selection state. Neither a feed refresh nor a prober reachability success automatically clears a suspension imposed by actual workload failures. A globally reachable node can remain suspended for one region/workload/profile. Report local suspension reason, scope, expiry and last-resort use as operational diagnostics, separately from public availability. Candidate generation changes must not create needless stream interruptions.

The maintainer selected a bounded background local discovery loop with accelerated discovery when needed (option B). Keep an independently usable fallback set prepared before the current pool is exhausted. Proposed scheduling separates less frequent endpoint enumeration from freshness/eligibility checks of known endpoints, and accelerates on low usable headroom, capacity rejection, rising failures, or feed problems. Define usable headroom per gateway region and requested workload/capability, subject to price, trust, payment, and suspension policy; global inventory size is not a substitute. Stabilize acceleration with backoff/jitter, bounded concurrency, and explicit normal/recovery budgets. Exact triggers, intervals, minimum headroom, and budget ceilings remain open and require measurement; no claim about current discovery cost has been benchmarked. Relatively stable topology does not establish current available worker capacity.

### Regional performance and health

Retain endpoint identity, actual observer identity/location, observer region, workload/capability, measurement source, timing boundaries, and observation time with performance results. A worker reachable from multiple regions has separate path results. One poor regional path must not lower another region's selection score or suspend/remove the worker globally. Preserve gateway-level evidence within a region because gateways in that region may also disagree; an affected gateway or network path does not prove a worker fault. Unknown regions have no measured performance, rather than inheriting another region's result.

Public comparisons and map details must offer regional results, sample counts, provenance, and freshness. A pin can be globally reachable while its path from a selected observer region is degraded. Preserve workload and production-versus-synthetic distinctions when comparing results. Any proposed global summary must disclose its coverage/weighting and cannot replace regional serving scores. Findings distinguish observer/path degradation from corroborated worker-level problems; cross-region agreement alone is not conclusive root-cause proof. Global exclusion requires a separately justified policy such as an authenticated identity failure or explicit configured exclusion, rather than a slow regional measurement.

## Availability semantics

Keep **observed discovery success** (successes/completed attempts) separate from sampled endpoint availability and production transcode reliability. A responding but full/incompatible orchestrator can be reachable without being usable for a particular job.

For an evidence interval:

- **Up:** a fresh attributable discovery succeeds from any healthy gateway/prober. Preserve other paths' failures independently.
- **Unavailable from checked locations:** no fresh success and all assigned checks complete qualifying failures from healthy observers; one assigned observer is sufficient. This is not proof of a global physical-worker fault.
- **Unknown:** missing/unhealthy observers, incomplete assignments, ambiguous attribution or no valid coverage. DNS-only evidence is not a successful check.

Routine RPC cadence is 60 seconds, with jitter and bounded concurrency. Backoff after completed qualifying failures follows a proposed 1/2/4/8/15-minute schedule; success resets it. Missing results and observer failures do not increase an endpoint's failure backoff. Emit expected due times, completion deadlines, effective cadence and actual observation timestamps. Initial current-state freshness proposal is two expected checking intervals; historical bins use event-time evidence and late results never become current success just because they arrived recently.

Do not present sparse/backed-off sampling as continuous uptime. Show observation coverage, cadence and gaps with availability. An attempt-success ratio can be biased by testing reachable endpoints more frequently; do not call that time uptime. Initial history uses actual observed 60-second evidence bins with unsampled periods unknown. Any later time-duration interpolation must be explicit and bounded, and comparisons must expose coverage. Keep last-success age, observed failure/recovery spans and production deadline consistency visible so a sporadic success cannot establish sustained transcode suitability.

Maintain minimal monitoring records for active on-chain orchestrators and currently advertised/discoverable endpoints indefinitely while that current reference persists, including long failures. The 30-day abandoned-endpoint recovery window governs cleanup only when no current catalog/DNS/advertised reference remains. Chain/provider read failure cannot be interpreted as authoritative removal. A failed RPC alone does not renew success history; a fresh catalog/DNS reference establishes current monitoring relevance, not availability. Detailed analytics still expire under finite TTLs.

Each prober resolves the full hostname catalog every five minutes, retaining observed answer/TTL/source facts and the union across time/regions. Known endpoints are tested independently of individual DNS responses. Catalog refresh is proposed every 15 minutes with jitter, using the last validated cache during provider outages. Unseen/rediscovered endpoints receive bounded verification and resume the same identity where retained evidence supports it.

Keep reachable/degraded/capacity-constrained/incompatible/stale/unknown states distinct. Gateway/prober or ingestion failures are observer/pipeline evidence and cannot justify a remote-worker outage. Observer assignments and map status remain independent of gateway selection/suspensions and ops-incident policy.

## Reference sources and discovery jobs

The inspected `~/repos/LivepeerPerformanceTranscoder` React/Leaflet source is a visual/metric reference. The referenced site and its `/orch/json` feed are outside implementation scope: no redirect, repoint, migration or consumer compatibility is required. The live site could not be fetched and no browser connection was available; no live behavior is claimed verified.

The related `~/repos/OrchestratorAvailability` source contains Node hodler/prober code. Reuse understanding of its operational responsibilities to consolidate discovery into Go; its external leaderboard data and public feed do not define our data model. The current prober resolves one IP separately from its hostname gRPC call, so it can misattribute the responding endpoint. This is a source limitation to avoid in the replacement, not permission to edit that external repository.

### Separate discovery and test jobs

| Job                     | Input                                                          | Result                                                                                      | Scheduling purpose                                                             |
| ----------------------- | -------------------------------------------------------------- | ------------------------------------------------------------------------------------------- | ------------------------------------------------------------------------------ |
| Resolve endpoints       | Orchestrator identity, hostname/port, observer/resolver        | Observed A/AAAA/CNAME answers, available TTL/source, timestamps                             | Full catalog on every prober every five minutes; accumulate the endpoint union |
| Test endpoint discovery | Concrete endpoint/IP, original authority and expected identity | Actual remote socket, discovery response or classified failure, timings/prices/capabilities | Verify/retest known endpoints independently of individual DNS answers          |

DNS yields candidates, not proof of availability or every worker behind a proxy. Direct tests preserve authority and protocol authentication; redirects/sub-node advertisements become candidates rather than falsely crediting the requested instance. Shared IPs/proxies need an optional verifiable instance identifier before claiming individual physical-worker uptime. Missing/expired assignments are coverage gaps. Stable event/attempt IDs, bounded concurrency and deadlines keep results attributable and replay-safe.

For located endpoints, one linear scan selects the closest healthy located observer and a second only within twice the closest distance. Proposed factor 2 is configurable, not a universal physical cutoff. Distances 100/180 select two; 100/7,000 select one; 1,800/2,200 select two. Zero distance admits only colocated observers without division. If every observer is far away, use the best available and disclose vantage/distance rather than labelling it local. No healthy observers means unknown coverage, not exclusion from the dashboard.

Unknown endpoint geography still gets a visible unlocated entry and one healthy fallback observer. Reuse existing discovery timing/path evidence for a coarse 'best observed from region X' hint, with source, age and uncertainty; do not turn the lowest RPC time into a claimed physical coordinate. If sufficient multi-vantage connection/transport timing evidence becomes available, a coarse inferred-region field can be added separately from authoritative/GeoIP location. A single ping cannot establish a unique location, and full RPC duration includes server/handshake effects. Keep assignment geography-only for located endpoints; this fallback does not silently introduce timing-based scheduling. No dedicated ICMP sweep is required initially. See [CAIDA's geolocation comparison study](https://www.caida.org/catalog/papers/2011_geocompare_tr/geocompare-tr.pdf) for the ambiguity of RTT affected by path and congestion.

Track DNS-seen, last-test, last-success and catalog-reference clocks separately. An active catalog hostname remains resolved/tested even when old endpoint history expires. Currently advertised endpoints retain minimum state and backed-off checks beyond 30 days; absent failed endpoints age out after 30 days and can be rediscovered. Map hiding does not stop assignments or remove gateway candidates.

Actual gateway jobs supply production performance. Discovery checks measure discovery, not transcode throughput. No synthetic transcoding or paid benchmark jobs are initial scope. Staging observations remain staging. Managed self-hosted resources use Foghorn rather than joining the on-chain prober catalog.

The marketing map already has fullscreen support and public topology/orchestrator reads; the app `/network` delegates to federation. Build the dedicated main-site Network page and app/operator views on the shared permitted API and metric definitions. Sharing an API does not require React/Svelte to share every component.

## Dashboard

Deliver `frameworks.network/network`, related app/operator views and public API reads. Show observed Livepeer compute alongside explicitly public FrameWorks infrastructure; retain internal cluster/stream diagnostics in authorized views. Livepeer public browsing needs no account or wallet association. Filter by observer region, workload/profile/capability, price, coverage, current status and environment where appropriate; production views never blend staging data.

Approximate GeoIP pins disclose source/freshness; unknown locations remain available in the list and totals rather than disappearing or receiving invented coordinates. Show the best observed vantage or labelled inferred region when evidence supports it. Endpoint rows disclose attribution certainty, samples, last check, last success, cadence and coverage. Multiple endpoints within a region remain separate.

Grey a pin after the first complete qualifying unavailable interval; missing coverage has a distinct unknown/stale presentation. Hide endpoints without success for seven days by default, with Show inactive/API filters. Hiding never stops monitoring. Abandoned unreferenced endpoints expire after the 30-day recovery window; active/currently advertised endpoints stay discoverable in the inactive view while minimal current monitoring state exists. Successful attributable discovery restores normal cadence and visible reachable state. Analytics TTLs are independent of display and catalog lifecycle.

Compare actual transcodes within observer region and workload/profile cohorts. Include volume, tail latency, source-duration-normalized speed, deadline-miss rate, errors, retry/fallback rate and sustained slow/failing streaks. Discovery success is not transcode reliability; no production samples means unknown performance. A global wallet average or occasional fast job must not obscure endpoint/path variation and consistency required by long-running live streams. No universal leaderboard score or synthetic fixture workload is initial scope.

Provide a gateway selection funnel: catalog-known, discovered, eligibility accepted/rejected, pool admission/suspension, selected and actually used. Report observed reasons such as discovery timeout/slow response, unsupported capability, configured price/payment requirement, suspension or lower selection preference. Distinguish not evaluated, no recorded reason and not selected; absent use alone is not poor performance. Reasons retain time, region, stage and rule context rather than becoming global endpoint labels.

Operator detail correlates logical segments and session changes with replacement discovery/setup time, final elapsed/budget outcome, late/missing renditions and MistServer delivery evidence where available. Public views expose safe aggregate network metrics without customer stream/session identifiers. Publish price/capability history for the selected 30-day detail window with exact units and validity; no extended pricing history or fiat assumptions. Existing longer finite rollups expose only what they actually retain.

## Dashboard evidence, impact and incidents

Initial network observations belong in Periscope/read projections and dashboard diagnostics. A failed discovery, price change, unused orchestrator or poor regional score is not by itself an incident and does not create a Livepeer notification. No separate initial Lookout network-finding lifecycle, automatic reminders/escalation or orchestrator email/contact feature is required.

Collect evidence needed for actual ops impact: logical segment deadline/budget misses and final failures, retry/hedge/session change outcomes, replacement setup delay, usable-pool headroom/exhaustion and late/missing media output. Connect gateway timings to MistServer output or delivery signals where instrumentation exists or must be added. A switch might be harmless; a failed attempt may recover within budget; a slow segment may not yield a proven viewer interruption. Preserve final outcomes and signal availability rather than asserting causation from timestamps alone.

Existing Lookout platform/tenant ops handling can use actual delivery/pool impact evidence under its ordinary ownership and severity policy. Future new alert rules must use calibrated effect on our service, not three failed network checks or a public score threshold. Standalone Livepeer notifications and automatic network escalation stay deferred. An existing ops alert about impacted FrameWorks streaming remains ops even when Livepeer is involved; this does not enable orchestrator-status notification spam. Avoid one incident per individual retry/segment/orchestrator.

The gateway source already retries failed segments on different orchestrators and `recordRoundTrip` suspends after consecutive round trips exceed source segment duration. Export structured decision/transition and final-outcome evidence rather than infer those actions from performance scores. Its metadata transcode event includes attempts and final success, but the audited FrameWorks per-attempt pipeline does not establish a complete MistServer delivery/impact correlation. Add that measurement contract explicitly; do not claim end-to-end interruption detection already exists.

### Future diagnostic evidence

Repeated allocation failures may indicate load, configured capacity, encoder/session limits, worker failure or memory pressure. A driver patch diagnosis requires direct evidence absent from today's discovery contract. Display classified symptoms and uncertainty, not accusations that an operator forgot a patch. Optional upstream instance/hardware/driver/effective-capacity extensions can be reviewed later; external orchestrators are outside our control.

External contacts, email outreach, wallet ownership proof, synthetic benchmark jobs and remediation automation are deferred. These are future possibilities, not prerequisites for public insight or this implementation plan.

## Implementation phases and acceptance

| Phase | Deliverable                               | Acceptance                                                                                                                                                                             |
| ----- | ----------------------------------------- | -------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| 0     | Planning decisions and source audit       | Batch corrections incorporated; active monitoring, public insight and impact-versus-dashboard boundary explicit                                                                        |
| 1     | Ops/business Lookout destination split    | Independent webhook fixtures, stable retry identity and legacy pending-row draining; no Livepeer deliveries                                                                            |
| 2     | Attributable durable gateway telemetry    | Stable replay IDs, deduplication before aggregates, correct measurement boundaries, selection dispositions and final segment/pool impact evidence                                      |
| 3     | Generic Go coordinator/prober replacement | One production catalog, full-catalog DNS, targeted RPC attribution, adaptive failures, shared-secret ingest and continued active monitoring; manual external host deployment supported |
| 4     | History, public/operator API and views    | Thirty-day detail/price history, existing finite rollups, no account requirement for public Livepeer insight, scoped operator correlation, explicit gaps and unused-candidate reasons  |
| 5     | Gateway shared-feed rollout               | Shadow validation then supplementation; hard eligibility/session/suspension behavior and local fallback preserved under feed failure and pool pressure                                 |

Some phases can progress independently; notification splitting is useful without the observer rollout. Evaluate usable pool and stream-delivery impact through existing ops mechanisms; new impact thresholds require measurement/calibration rather than an arbitrary orchestrator-unavailability rule. No legacy website change, synthetic transcode scheduler, testnet support, Livepeer notification, new escalation engine or contact/outreach implementation is included. Scheduling/storage defaults are subject to volume verification without reopening settled product scope.

### Concrete implementation work packages

1. **Lookout routing:** extend settings and explicit activity event mappings; add stable destination identity to both outboxes and uniqueness; preserve compatibility for queued rows and existing email/tenant policies. Reuse Slack/Discord builders and senders. Configure ops/business URLs through existing config paths, with no Livepeer route activation.
2. **Shared telemetry contracts and gateway hooks:** extend protobuf/event sources for actual endpoint attribution, measurement presence/units, source duration, stage/disposition reasons, selection/pool transitions and final logical segment outcomes. Export structured evidence at discovery, eligibility, suspension, retry/session setup and output boundaries. Keep detailed customer correlation private. Update compatible shared package dependencies in go-livepeer; generated files follow repository codegen rules.
3. **Durable ingestion/projections:** move gateway sends off media paths into bounded spool/queue delivery; propagate stable producer IDs through Decklog and acknowledge committed publication. Implement/prove deduplication before additive analytics aggregation, separating duplicate delivery from distinct workload attempts. Expose spool/drop/observer gaps. New centralized probe ingestion uses the same stable-ID contract.
4. **Observer module:** add the single central/regional Go module and its owned catalog/inventory/assignment schema. Read the production Livepeer contract catalog; run full-catalog DNS and targeted attributed RPC jobs, adaptive failure backoff and active-reference-aware cleanup. Expose shared-secret validated ingest and a versioned candidate feed. Support manual binary/config deployment on external prober hosts without a CLI enrollment feature.
5. **Periscope history/public projection:** add detailed state/price and decision/impact facts with explicit thirty-day retention, bounded distributions and coverage. Keep existing finite rollups. Separate sanitized public Livepeer aggregates from tenant raw facts and private infrastructure, with production/staging isolation and supported expiring-detail behavior.
6. **Bridge/read surfaces:** expose public network and authorized operator evidence through existing client/GraphQL/API contracts, bounded filters/window sizes and audience-aware caches. Public Livepeer reads need no account/wallet proof. Preserve tenant filters and service ownership; no cross-service DB reads.
7. **Public/app UI:** build the main-site Network page plus app/operator views using shared definitions. Add endpoint/path comparisons, unknown/unlocated coverage, inactive toggle, price/performance history and gateway selection reasons. Operator drill-down includes private final-output/failover correlation; no changes to the referenced external dashboard.
8. **Candidate rollout and ops evidence:** exercise local fallback/Redis/selection/session invariants under feed and capacity failures; shadow then supplement. Correlate gateway delay with available MistServer output signals before asserting interruption. Existing impact-based ops policies remain authoritative; additional thresholds are calibrated separately from network-status observations.

## Validation scenarios

Use focused regression tests and extend existing stack fixtures. For any current behavior treated as a bug during implementation, capture a failing assertion before its fix and record the green result afterward. These are planned assertions, not tests claimed to have run:

1. Enqueue an ops incident and a signup: each reaches only its purpose's webhook; disabling business does not reroute it to ops. Retry after reload retains the original destination identity.
2. Upgrade with queued legacy rows: each drains once to its legacy destination; no Livepeer notification rows are created initially. Tenant incidents retain their existing delivery scope.
3. Publish one producer event, lose the RPC response, resend it, and redeliver Kafka: raw canonical facts and all aggregate attempts remain one. Distinct workload retries count as distinct attempts but one logical request.
4. Observe two wallet endpoints with different prices: preserve both and their history; unknown capability price identity stays positional; missing price remains unknown. A missing observation cannot render a zero-price step.
5. DNS returns three IPs but only one is dialed: one completed attempt, two context rows. Concurrent requests/dial-cache reuse cannot assign an outcome to a guessed sibling IP. DNS failure remains visible with unknown IP.
6. RPC returns successfully but downstream compatibility fails: reachable and incompatible both remain true to their respective facts. Capacity rejection remains distinguishable from unreachable transport.
7. Stop the gateway or Decklog: missed scheduled slots appear as unknown coverage and an ops telemetry finding; no orchestrator-wide outage notification is inferred. Restart/replay converges without duplicates.
8. Two stream tenants use one owner's gateway: operator aggregates include permitted sanitized totals; each tenant sees only its own detail. Public readers see external Livepeer network aggregates without raw customer facts or private managed topology. A private gateway topology does not prevent publishing sanitized measurements of the public external network.
9. Isolated or repeated allocation failures retain classified symptoms without asserting a driver patch cause or automatically opening a Livepeer incident. Available direct evidence and suspected explanations remain distinguishable.
10. Query historical windows after raw TTL expiry: use supported rollups and expose unavailable detail; do not invent percentiles, failure samples, or old prices.
11. Initial configuration and events cannot enqueue Livepeer Slack/Discord/email deliveries or contact outreach. Existing impact-based platform/tenant ops alerts retain their normal authorized destinations.
12. DNS jobs discover different endpoints for one hostname within one region and across regions/time: the inventory and regional candidate snapshots retain multiple applicable endpoints. Test each candidate directly with the original authority; an answer for endpoint A cannot mark B up. A disappeared DNS answer remains testable, a missing job result is unknown coverage, and map aging/hiding never deletes its history.
13. Seed a two-endpoint pool, then refresh with only one responding: demonstrate and record the current capability-list behavior before implementing per-endpoint freshness/merge policy. Test explicit removal separately from incomplete refresh; restart and feed/Redis outages must preserve the agreed eligible fallback set.
14. For identical normal, invalid-input, hedge-loser, and transport-failure attempts, assert analytics disposition and selection-cache effects separately. Source-duration/timing semantics must be consistent enough to reproduce the documented speed calculation without counting local/intentional cancellations as orchestrator failure.
15. Exercise cold start with an empty/unavailable feed, all feed candidates locally rejected, and a running feed becoming empty: a valid locally discovered candidate must still create sessions with correct pool sizing and ordinary checks. Feed recovery must preserve active sessions, trust distinctions, and suspensions; a prober success does not unsuspend an endpoint. Verify existing last-resort suspension behavior separately from hard exclusions and Redis outage handling.
16. One endpoint has good western-Europe results and slow/failing US-east-coast results: retain both series, keep global any-observer reachability, and apply selection/suspension consequences only to the affected scope. A gateway-local failure must not convict the entire region; an unobserved region remains unknown. Public comparisons and findings must expose the observed paths rather than a global bad-worker label.
17. Reduce usable candidates for one workload while the global catalog remains large: trigger bounded refresh before pool exhaustion, preserve ongoing sessions, and discover usable replacements under ordinary eligibility rules. Under a stable healthy pool, enumeration backs off within its budget without stopping required availability sampling. Record measured discovery costs and recovery latency before finalizing defaults.
18. Give every prober the same full hostname catalog, including two probers in one region: each publishes its own complete cycle/answers, and the inventory unions endpoints without requiring rotating DNS assignments. Targeted RPC assignments are independent; deduplicating inventory never fabricates endpoint-test successes or merges observer provenance.
19. A relatively distant second observer is omitted when a much closer primary exists. If all healthy observers are far away, retain best-available checks and disclose their vantages. Unknown-geo/no-observer endpoints remain in dashboard lists with honest location/coverage; no invented downtime or coordinates.
20. A self-hosted worker reports through its existing Foghorn connection: retain that observation source without enrolling it in the on-chain prober catalog. A lost control connection is distinguishable from a failed external endpoint RPC and a failed job; future AI/Edge observations retain separate workload metrics and pricing units.
21. Network evidence is visible on public/operator dashboards without creating a Lookout incident. A seamless within-budget failover creates metrics only; an observed late/missing-output or exhausted-pool case can support existing ops impact handling. Public readers see network aggregates without private stream correlation, and disabled routes never cross-purpose fallback.
22. Exercise generic inventory/observation contracts with a synthetic non-Livepeer resource: a provider-defined resource ID and protocol endpoint need no Ethereum wallet, DNS hostname or transcoding fields. Preserve explicit units/check kind, unsupported measurements and authorized provider routing. This is a contract fixture, not implementation or benchmarking of a future external storage network.
23. Verify relative-bucket distances 100/180 select two, 100/7,000 select one, and 1,800/2,200 select two for factor 2. Cover no healthy observers, unknown geo, zero-distance/tied coordinates and all-distant best-available fallback. No region receives a quality/downtime verdict from geography alone.
24. An active on-chain orchestrator/currently advertised failed endpoint remains checked at the backed-off cadence beyond 30 days; an abandoned unreferenced endpoint expires without renewing its last-success clock. Provider outage cannot remove the catalog. Map hiding changes neither assignments nor gateway availability.
25. Adaptive checks emit due/cadence evidence; unknown jobs do not cause backoff, success resets it, and sparse observations never fabricate continuous uptime. Replayed old success changes historical evidence without restoring current reachability.
26. Shared-secret ingest rejects unauthenticated/invalid/oversized input, validates configured observer/assignment labels and server scope, and deduplicates retries. A caller-provided owner/cluster never grants authority; manual external-host deployment has no CLI or client-certificate requirement.
27. Record concrete discovery/eligibility/suspension reasons; distinguish not evaluated and not selected from rejection. Compare the same endpoint across gateway regions without a global penalty. Distinct logical retries and final segment outcomes join privately; input faults/cancellations are not silently charged as orchestrator faults.
28. Final gateway retry/setup timing and MistServer late/missing-output signals correlate where available; missing downstream evidence stays unknown. A successful switch alone cannot prove interruption. Production/staging observations and price detail TTLs remain isolated and finite.

Run meaningful Go/DB tests through existing Make targets and frontend component checks for charts/formatting. Run `make test`, `make verify-prepush` on the exact implementation commit, and `make verify-stack` on remotedev. Use the Livepeer live/VOD scenarios as the base for telemetry fault/replay fixtures; use fake orchestrators and local notification receivers rather than contacting real operators. No nightly schedule or staging rollout is part of this task.

For schema work, update canonical baselines and migrations together under the next catalog-declared release at implementation time, then run `make validate-migrations` and `make verify-schema-migrations`. The inspected catalog ends at `v0.3.11`; this RFC reserves no future version. Change protobuf sources and run `make proto`; never edit generated contracts. GraphQL regeneration must be run locally by the maintainer as required by `AGENTS.md`, before resolver changes that depend on the new generated schema.

## Dependencies and alternatives

Implementation touches go-livepeer discovery/result hooks and optional health contracts; Decklog RPC conversion; Periscope ingestion, ClickHouse layout and read APIs; Bridge authorization/GraphQL operations; federation UI; Lookout ops/business routing and delivery; and CLI/gitops config templates. Pin the compatible published monorepo `pkg` dependency in go-livepeer when the shared contract changes. The sibling checkout was inspected only and is not modified by this planning task.

Prometheus remains useful for pipeline health and bounded service metrics. It is a poor sole store for per-attempt evidence, exact prices, and high-cardinality endpoint history. A separate notification service adds deployment/ownership work before Lookout's existing delivery path has been extended. Public comparison is in scope; a single overall leaderboard score is excluded initially because traffic mix and incomplete coverage affect rankings. Region/workload filters and sample counts must accompany comparisons.

Upstream go-livepeer PRs may improve standard GPU information, instance identity, and structured errors. Track those changes as optional extensions: external orchestrators are outside platform control, and deployment of an upstream patch cannot be assumed. The baseline feature must remain useful with today's protocol, marking absent fields unknown.

Preserve existing gateway selection during the initial telemetry rollout. Review the candidate-feed redesign explicitly, including exploration, sparse/new orchestrators, stale scores, eligibility, and outage fallback. Publishing public comparison metrics does not automatically make them the gateway's ranking policy.

## Risks and mitigations

- **False outage or diagnosis:** coverage, origin classification, independent observers, evidence windows, and observe-only calibration.
- **Tenant/public scope errors:** distinct raw and sanitized projections, cluster-aware filters, audience-aware caches, and explicit isolation scenarios.
- **Telemetry backpressure or duplicate counts:** bounded asynchronous spool, observable gaps, stable IDs, deduplication before additive aggregation.
- **Misleading price comparisons:** exact units, request/model context, freshness, positional labels, and no reconstructed historical quotes.
- **Notification noise:** split ops/business destinations; defer standalone Livepeer alerts and new escalation rules, and distinguish network evidence from actual service impact.
- **Storage growth:** measure event rate and state churn, retain compact changes, control cardinality and maximum query windows.

## Batch decisions recorded on 2026-10-04

The maintainer answered the twelve-area batch. These selections supersede earlier proposed defaults and any historical menu language in the work log.

| Area                    | Selected direction                                                                                                                                |
| ----------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------- |
| 1. Inactive checks      | C-style adaptive failure backoff; never stop monitoring active on-chain/currently discoverable resources; normal cadence resumes on success       |
| 2. Coverage/location    | A best-available distant/fallback coverage; keep unlocated resources visible; coarse path-based location hints only with disclosed uncertainty    |
| 3. Catalog              | One production Livepeer network; no testnet implementation                                                                                        |
| 4. Gateway feed         | A shadow validation then supplement local pools with validation, caches, suspensions and fallback intact                                          |
| 5. Measurement/delivery | A attributable corrected measurements, durable bounded spool, stable IDs and deduplication before aggregates                                      |
| 6. Public insight       | Public permissionless Livepeer metrics with no account/wallet/opt-in requirement; scope private customer and managed-resource evidence separately |
| 7. Surfaces             | Dedicated main-site/app/API network views; reference dashboard migration/redirect entirely outside scope                                          |
| 8. Performance source   | Actual production transcodes; staging stays staging; synthetic transcoding deferred                                                               |
| 9. History              | Thirty-day detailed observations/state/prices; no extended price history; existing finite rollups retained                                        |
| 10. Probers             | Shared narrow ingest secret over TLS, manual deployment on external hosts supported; no CLI/per-observer certificate enrollment requirement       |
| 11. Notifications       | Split existing ops/business destinations through current senders; generic later purpose capability, no Livepeer notifications initially           |
| 12. Incidents           | Dashboard includes network/discovery/selection problems; ops incidents require effect on our gateways/streams under existing policies             |

Implementation proposals to validate against source and measured volume: failure doubling up to 15 minutes, two expected intervals of status freshness, catalog refresh every 15 minutes, connection-scoped attribution mechanics, explicit discovery/selection dispositions, impact signal/correlation hooks and durable projection design. These are engineering details, not twelve new product menus. No need to inspect the deployed prober fleet to define the relative-distance algorithm. Exact physical worker identity remains limited by external protocol facts; no account-linking feature or optional upstream extension is required to deliver useful public endpoint insight.

Planning selections do not implement runtime behavior, deploy binaries, change external repositories, configure real chat channels, contact orchestrators or publish remote refs.

## References and evidence

- [Reference] [Orchestrator visibility architecture](../architecture/orchestrator-visibility.md).
- [Reference] [Lookout architecture and operator activity](../architecture/lookout.md).
- [Reference] [Bosun public webhook ownership](../architecture/bosun.md).
- [Reference] [Analytics pipeline](../architecture/analytics-pipeline.md) and [service events](../architecture/service-events.md).
- [Evidence] Gateway [telemetry producer](../../../go-livepeer/monitor/frameworks/telemetry.go), [discovery emission](../../../go-livepeer/discovery/discovery.go), [DB-cache polling](../../../go-livepeer/discovery/db_discovery.go), [outcome emission](../../../go-livepeer/server/segment_rpc.go), and [RPC/dial attribution](../../../go-livepeer/server/rpc.go) in the sibling checkout. These links require that checkout beside the monorepo.
- [Evidence] Upstream [hardware and discovery contract](../../../go-livepeer/net/lp_rpc.proto).
- [Evidence] On-chain catalog and service-URI reads in the sibling [Ethereum client](../../../go-livepeer/eth/client.go), reused by [DB pool discovery](../../../go-livepeer/discovery/db_discovery.go).
- [Evidence] Shared [telemetry contract](../../pkg/proto/ipc.proto), [client envelope/send behavior](../../pkg/clients/decklog/client.go), and [Decklog conversion](../../api_firehose/internal/grpc/server.go).
- [Evidence] [Tables and retention](../../pkg/database/sql/clickhouse/periscope.sql), [ingestion](../../api_analytics_ingest/internal/handlers/orchestrators.go), and [queries](../../api_analytics_query/internal/grpc/orchestrators.go).
- [Evidence] [GraphQL network scope](../../api_gateway/internal/resolvers/orchestrators.go), [federation page](../../website_application/src/routes/infrastructure/federation/+page.svelte), and [orchestrator details](../../website_application/src/lib/components/dashboard/OrchestratorSidePanel.svelte).
- [Evidence] Lookout [settings](../../api_incidents/internal/notify/settings.go), [router](../../api_incidents/internal/notify/router.go), [activity allowlist](../../api_incidents/internal/notify/activity.go), [dispatcher](../../api_incidents/internal/notify/dispatcher.go), [persistence](../../pkg/database/sql/schema/lookout.sql), and [configuration](../../api_incidents/internal/appconfig/lookout.go).
- [Reference] [Remote development](../development/remote-development.md), [Makefile](../../Makefile), [remote-dev wrapper](../../scripts/remote-dev.sh), and [release catalog](../../cli/internal/releases/catalog.yaml).
- [Evidence] Local legacy frontend: `~/repos/LivepeerPerformanceTranscoder/src/{pages/home.js,util/api.js,hooks/useProcessedData.js,components/WorldMap.js,components/RankingTable.js}`. Legacy collector/probers: `~/repos/OrchestratorAvailability/master/{worker.js,src/routes/hodler.js}` and `~/repos/OrchestratorAvailability/client/src/util/orchTester.js`.
- [Evidence] Marketing public map/feed: `website_marketing/src/components/marketing/network/{NetworkMap.jsx,useNetworkStatus.js}`. Webapp public route: `website_application/src/routes/network/+page.svelte`.
- [Evidence] Gateway Redis scores/suspensions: sibling `../go-livepeer/server/{orch_perf.go,suspensions_redis.go,selection.go}`. Local discovery snapshots and refresh behavior: `../go-livepeer/discovery/{snapshot.go,db_discovery.go,snapshot_test.go}` and `../go-livepeer/common/db.go`. Gateway provisioning of regional Redis: `cli/cmd/cluster_provision.go`.
- [Evidence] Trusted/untrusted session pools, suspension last-resort admission, and session selection in [gateway broadcast handling](../../../go-livepeer/server/broadcast.go), with suspension ordering in [discovery](../../../go-livepeer/discovery/discovery.go).

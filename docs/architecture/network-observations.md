# External network observations

FrameWorks publishes public Livepeer inventory, discovery, quotes and real transcoding
performance at `/network` on the marketing site and web application. Bridge exposes the
same projection at `GET /v1/network/livepeer`. The source is FrameWorks gateways and
trusted external probers. This does not use Livepeer Studio's performance API.

## Ownership

`api_network_observer` contains two Go commands:

| Component                 | Responsibility                                                                                                         | State                                                                                       |
| ------------------------- | ---------------------------------------------------------------------------------------------------------------------- | ------------------------------------------------------------------------------------------- |
| `network-observer`        | Read the contract catalog, combine DNS results, schedule endpoint checks, ingest observations, publish candidate hints | Its own tenant-scoped PostgreSQL database; HTTP 18024                                       |
| `network-prober`          | Resolve the complete catalog locally, execute assigned direct-IP discovery RPCs, durably report results                | Private bounded local spool; health HTTP 18025                                              |
| Livepeer gateway          | Report actual discovery sockets, current state, selection decisions, attempts and final segment outcomes               | Private bounded local spool; its existing discovery/pools/Redis caches remain authoritative |
| Decklog / Periscope       | Commit observations to Kafka; retain deduplicated history and bucket snapshots; serve analytics                        | Existing analytics services and ClickHouse                                                  |
| Bridge / shared dashboard | Enforce publication and operator access; render inventory and regional comparisons                                     | Server-owned publication scopes                                                             |

The previous Hodler functions are absorbed by the coordinator's catalog adapter and
scheduler. The external prober command replaces the discovery/reporting functions of
OrchestratorAvailability. Neither legacy repository is changed or redirected by this
feature. Install the new external command manually; it is not a managed Edge node and
requires no FrameWorks CLI enrollment. Managed selfhosted workers remain attached to
Foghorn and are not inserted into the onchain catalog.

Inventory, assignments and observer contracts are provider neutral. Livepeer contract
and discovery protocol details live in `internal/providers/livepeer`. Additional
providers can implement catalog/check adapters; their workload and pricing units must
remain separate. This version records transcoding only, with no synthetic paid work.

## Discovery and instance identity

The production adapter verifies chain ID 42161 and reads BondingManager and
ServiceRegistry through the configured controller at a fixed block. A failed refresh
preserves the last catalog. Production and staging observations have distinct namespaces.
Every healthy prober resolves the complete active catalog every five minutes. The
coordinator unions answers across regions; one hostname can have several IPs even in
one region. DNS resolution and assigned discovery RPCs run independently.

An endpoint combines provider, network, environment, onchain identity, URI authority
and IP. A successful discovery check must verify the orchestrator identity and the
actual connected socket IP. Direct-IP dialing preserves the original hostname for
RPC authority and TLS. A shared proxy IP is an observed endpoint, not proof of a
physical GPU worker. Gateway discovery contributes only the socket it actually dialed;
DNS siblings do not receive credit for its success.

The scheduler uses endpoint and prober geolocation when available: assign the nearest
healthy prober and add the second if its distance is no more than twice the nearest's.
There is no fixed kilometer exclusion. A distant nearest prober still gives coverage,
with its region exposed in comparisons. Unknown geography gets one healthy fallback
prober, stays in the inventory and receives no invented map coordinates. Prober source
IP comes from the connection; forwarded addresses are honored only through explicitly
trusted ingress CIDRs. Optional configured locations can override lookup.

Checks normally run every minute. Completed qualifying failure cycles back off through
2, 4, 8 and 15 minutes; healthy success resets the cadence. Active, discoverable
orchestrators keep receiving tests. Missing, unhealthy or unattributable reports do not
count as orchestrator failures. Any recent attributable success from a healthy observer
makes the endpoint up; a complete assigned failure cycle means unavailable from the
checked locations. A bad path from one region does not establish global poor performance.

## Performance and stream impact

Discovery latency, submission-to-response time, response headers/body time, source
segment duration, profiles, attempts, retries, fallbacks, final failure and deadline
misses are separate facts. End-to-end response duration is not a GPU compute time.
Comparisons retain observer region, workload and profile dimensions. Input faults,
local cancellation and losing hedges do not count as qualified orchestrator failures.
Selection rejections and pool suspensions remain visible even when no transcode reaches
an orchestrator. Unattributed failures stay unattributed rather than borrowing a DNS IP.

Final segment records retain private session/sequence correlation and replacement setup
time. Operator diagnostics can add existing MistServer output receipts only on an exact
customer tenant, delivery session and sequence match. A receipt establishes output
observed, not seamless viewer delivery. Missing receipts remain unknown. This feature
creates no Livepeer incidents, orchestrator contacts, emails or other notifications.
Capacity errors do not establish an unpatched Nvidia driver.

## Reliability and retention

Gateways and probers assign stable IDs before enqueueing observations. Spools persist
across restart, default to 64 MiB and 10,000 records, and retain undelivered records for
at most 30 days. Use one process per spool directory on persistent storage. Overflow and
expiry increment loss counters exposed in logs/health reports; this bounds reliability
rather than promising indefinite delivery. Coordinator ingestion atomically stores
state, receipt digest and telemetry outbox. Exact retries acknowledge the same batch;
conflicting reuse of an ID is rejected. Outbox removal follows committed Kafka delivery.

The canonical ClickHouse observation table deduplicates by stable event ID before
analytics. Rollups replace whole bucket snapshots using a distributed namespace lease;
replays never add another count. Rebuilding excludes a bucket whose start predates the
raw retention boundary, so expired partial source data cannot shrink an older snapshot.
The rebuild loop runs every five minutes.

| Data                                         | Retention |
| -------------------------------------------- | --------- |
| Raw observations, quotes, state and coverage | 30 days   |
| Five-minute performance/discovery snapshots  | 180 days  |
| Hourly discovery snapshots                   | 365 days  |
| Hourly workload/decision snapshots           | 730 days  |

Long-range API queries use hourly snapshots. Raw samples and pricing history are limited
to their retained window; missing history is not zero or evidence of downtime. Responses
mark rolled-up and truncated series. Whole-window comparison summaries are queried
separately from bounded chart points. Current inventory retains referenced endpoints;
unreferenced endpoints age out after 30 days without success. The default map hides
endpoints with no success for seven days; `include_inactive=true` includes them.
Inactive catalog metadata with no retained endpoint also expires after 30 days without
a catalog refresh. Identities still in the current contract catalog keep their minimal
inventory metadata.

V3 gateway state/discovery also refresh the existing non-additive current views, using
observation time as the replacement version. V3 samples do not enter legacy additive
materialized views. Historical legacy performance queries remain a V2 history surface;
the new REST/dashboard projection is the canonical V3 performance surface.

## Publication boundary

Bridge selects platform tenant scopes from `NETWORK_PUBLIC_TENANT_IDS`; a caller cannot
supply a tenant or request private data on the public route. Public history includes
network facts, quotes, coarse errors and regional results. Customer/session identifiers,
manifest hashes, delivery correlation, detailed selection context, observer identities
and endpoint assignment internals are removed. Existing platform infrastructure map
visibility remains an independent policy and is not made public by this API.

`GET /v1/network/livepeer/diagnostics` requires an interactive platform-operator JWT,
audits access and uses `Cache-Control: no-store`. A tenant administrator role alone is
insufficient. Periscope accepts the projection RPC only as an internal service call with
explicit validated publication scopes. When historical analytics is unavailable, Bridge
can still return live inventory with `history_status=unavailable`.

## Candidate hints and rollout

The coordinator feed is optional. Gateways default to shadow mode and continue their
local contract discovery, pool health, suspension, ticket, pricing and compatibility
checks. Supplement mode adds fresh URLs only for identities already eligible in the
local onchain set. Missing, stale, malformed or empty feeds preserve local candidates;
the coordinator cannot strand a gateway by returning an empty set.

The gateway fork's local `replace` of `monorepo/pkg` points to the adjacent feature
checkout for review. Pin a published compatible package revision before building that
fork independently. No upstream changes, deployment or remote testing are implied by
this workspace implementation.

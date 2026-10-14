# External network observations

FrameWorks publishes public Livepeer inventory, discovery, quotes and real transcoding
performance through the Livepeer tab at `/network?view=livepeer` on the marketing site
and web application. The default FrameWorks tab keeps infrastructure topology visible.
Bridge exposes the
same projection at `GET /v1/network/livepeer`. The source is FrameWorks gateways and
trusted external probers. This does not use Livepeer Studio's performance API.

These platform changes target `v0.3.13` and require CLI `v0.3.13` or later.
Existing clusters apply the `v0.3.13` ClickHouse expand migration. The new observer
database is initialized from its baseline by the release's database bootstrap.

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

The coordinator is one service per platform, declared in the cluster manifest
(`network-observer`, or the `network-observations` service-selection profile). It is not
added to clusters that run gateways by default: a platform has one coordinator in the
aggregator region, with its own database, and publishing Livepeer observations is a
platform decision, so a self-hosted platform opts out by not declaring it. Enabling it
needs only that declaration, its database entry and the generated
`NETWORK_OBSERVER_SECRET`; provisioning derives the chain Controller, namespace, tenant,
mesh address and Bridge configuration. Native and Docker deployments use the standard
platform artifact lifecycle and gate `v0.3.13` readiness on `/ready`. Declare
`network_observer` in the managed SQL database inventory; the existing release bootstrap
initializes its owned baseline and runtime role. The coordinator binds aggregator Kafka
and consumes local and source-prefixed mirrored analytics topics, so gateways in every
configured region contribute inventory. Its Decklog dependency also resolves in the
aggregator region.

Readiness on `/ready` answers 200 only while the database answers, the contract
catalog has refreshed, and the gateway consumer group is joined with assigned partitions
whose lag is consumed; the JSON body lists each failing reason. The windows derive from
the catalog refresh interval: a startup grace of one interval plus the two-minute refresh
timeout, catalog staleness after two intervals plus that timeout (one failed refresh is
tolerated), and two intervals for a failed group sample, a rebalance without partitions or
a lag without committed progress.

The coordinator has no public hostname. Bridge reads inventory over the WireGuard mesh
from `network-observer.<aggregator-region>.internal` on a second, internal-CA HTTPS
listener, verifying the leaf as `network-observer.internal`; the plain listener serves
health probes. Provisioning binds both listeners only to loopback and the host's WireGuard
address (`NETWORK_OBSERVER_BIND_ADDRS` natively, the published ports in Docker), with
FreeBind so the observer starts before the mesh interface exists; the LAN and public
interfaces do not answer. The coordinator registers with Quartermaster so the runtime mesh DNS lists
it, and the CLI-rendered Privateer seeds resolve and peer the alias on Bridge hosts.
External probers call Bridge's public `/network-observer/v1/work` and
`/network-observer/v1/observations` relay. Bridge passes the prober's bearer secret
through for the coordinator to verify, and replaces `X-Forwarded-For` with the address
it resolved. The coordinator trusts that header only from Bridge hosts' mesh addresses.

Linux prober release archives include the standalone installer, systemd unit and
example environment. They can be installed without a repository checkout. The
installer validates the private configuration and discovery identity before changing
the host. Successful coordinator connectivity alone does not prove discovery admission.

The legacy scripts demonstrate a working Ethereum-authenticated discovery path; their
data model and implementation are not compatibility requirements. The replacement reads
the contract directly instead of relying on the legacy subgraph, attributes measurements
to the socket actually checked, and keeps regional samples instead of extrapolating
continuous uptime between checks. Livepeer Studio scores, the legacy JSON/Prometheus
shape and ENS enrichment are not carried over. The separate old dashboard is outside
this migration.

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

External probes require a pinned public IP. Private, loopback, link-local and
other non-public destinations are rejected before dialing through the shared
destination policy. These policy rejections are not orchestrator failures.

Discovery uses the broadcaster's Ethereum address and a signature matching go-livepeer:
sign `accounts.TextHash(crypto.Keccak256([]byte(address.Hex())))` with the wallet key,
using the checksummed address representation. The prober receives only the address and
signature; it does not need the private key or sign transactions. Existing valid pairs
can be reused. An optional offline `sign-discovery` command uses go-ethereum's signing
and keystore libraries. The small discovery protobuf projection preserves go-livepeer's
wire field numbers without importing its media/server runtime.

Standard orchestrators can reject discovery for insufficient broadcaster sender reserve,
invalid authentication or capacity. These admission responses are retained as distinct
observations and do not count as qualifying endpoint downtime. No synthetic transcodes
are submitted. A successful configuration check proves signature validity, not remote
admission; provisioning needs an actual successful discovery check as well.

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

## Observed work and costs

Versioned gateway accounting separates dispatched attempts, returned reported pixels,
and final gateway-accepted segments. Dispatched source minutes include retries and
hedges. Accepted source minutes count each final logical segment once against its
selected orchestrator and socket; failed downloads and discarded responses do not
contribute accepted minutes. Rendition minutes derive from source duration multiplied
by the accepted rendition count, rather than independently measured output duration.
These facts describe work observed through FrameWorks gateways, not the orchestrator's
other customers, physical GPU utilization, or viewer delivery guarantees.

Service charges use the actual submission session's price multiplied by the pixels
returned for that attempt. They include charged results subsequently discarded or
rejected during download. Expected ticket credit sent is a separate observation at
the gateway's payment-submitted boundary, including non-success HTTP responses; an
ambiguous transport failure does not establish that credit was sent. Neither quantity
proves redeemed earnings. Values retain exact rational numerator/denominator strings
in ETH/wei; no current-price repricing, historical USD conversion or earnings estimate
is performed. Observations are assigned to windows by their completion timestamp.

Raw records retain optional versioned accounting. Missing accounting, unknown charges
and known zero charges stay distinct. The gateway's onchain ticket calculation uses a
256-bit probability fraction, so credit numerators are retained as strings and grouped
with their occurrence counts. Service-charge numerators have a bounded decimal
representation and remain grouped by denominator. The query service adds the retained
fractions using exact rational arithmetic.

`history.work` supplies full-window summaries by identity, observed IP, region and
profile independently of bounded chart samples. Consumers can sum sibling endpoints
for identity totals. Counters that can exceed JavaScript integer precision are decimal
strings. The shared dashboard uses integer arithmetic for aggregation and sorting.
If more than 10,000 work cohort/service-rate groups are required, `work.truncated=true`
returns no work summaries rather than incomplete totals. Exact ticket-credit values
have a separate 10,000-group bound: `work.ticket_credit_truncated=true` preserves all
work and service charges, omits credit amounts and marks affected summaries with
`ticket_credit_incomplete=true`. No partial credit sum is returned. Narrow the time window,
resource or observer region to reduce the query. Older records without accounting are
counted as unaccounted; they are not backfilled from advertised price quotes.

Work snapshots use the same stable-ID deduplication, namespace lease and whole-bucket
replacement as performance snapshots. Five-minute work snapshots retain 180 days and
hourly work snapshots retain 730 days. Long-range work windows cover only whole hours
contained in the requested interval, exposed separately in `work.start` and `work.end`.
Snapshots refresh every five minutes; they are observation analytics, not a billing
settlement ledger. Existing quote history retains its 30-day bound.

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

Gateway-keyed snapshots follow the same rows: five-minute 180 days, hourly discovery 365
days, hourly attempts 730 days.

Long-range API queries use hourly snapshots. Raw samples and pricing history are limited
to their retained window; missing history is not zero or evidence of downtime. Responses
mark rolled-up and truncated series. Whole-window comparison summaries are queried
separately from bounded chart points. Current inventory retains referenced endpoints;
unreferenced endpoints age out after 30 days without success. The default map hides
endpoints with no success for seven days; `include_inactive=true` includes them.
Inactive catalog metadata with no retained endpoint also expires after 30 days without
a catalog refresh. Identities still in the current contract catalog keep their minimal
inventory metadata.

## Network explorer

FrameWorks and Livepeer share a tabbed `/network` shell and preserve the map camera when
switching. Livepeer offers Compare and Map modes with shared filters, endpoint selection
and query-string deep links. Compare defaults to a compact map and sortable endpoint
table. Latency and failure rankings require one observer region and profile; they never
merge incompatible cohorts. Quotes sort by their exact rational price within matching
currency/unit groups; stale or missing quotes follow fresh ones. Quotes reflect the
observing broadcaster's offered price, not a guaranteed price for every broadcaster.

The Livepeer map has independent endpoint, gateway-cluster, prober and infrastructure
layers. Gateway telemetry carries no address of the gateway host, so the coordinator
places each gateway observer from Quartermaster once per catalog interval: at its node's
position (GeoIP of the node's external IP), or else at the centroid of the located nodes
in that node's cluster, the position the cluster layer draws. A gateway whose node and
cluster have no position, such as one on hosts with only private addresses, stays
unknown; region labels never become coordinates. A location in `NETWORK_OBSERVERS_JSON`
takes precedence. Registered probers with known coordinates appear individually;
unknown locations remain in coverage listings. The detail panel lists sibling endpoints
under the same onchain identity and retains regional discovery and performance history.

V3 gateway state/discovery also refresh the existing non-additive current views, using
observation time as the replacement version. V3 samples do not enter legacy additive
materialized views. Each gateway event is either V2 (legacy `orchestrator_*` tables) or
V3 (`network_observations`), never both, so the GraphQL `orchestratorPerformanceSeries`
sums the two without double counting. Its V3 part counts a gateway's raw discovery and
dispatched attempts exactly as the public projection does: every deduplicated row is a
sample; successes and failures count qualifying rows. Points stay keyed by gateway,
region and IP, so prober observations, whose observer identities are private, are
excluded. The region-keyed observation snapshots mix gateways and probers, so the rollup
loop also writes gateway-keyed snapshots (`network_gateway_rollups`) of each gateway's
discovery and attempts, with the same retention as the observation snapshots. The series
reads raw rows for the last 29 days and those snapshots, whole buckets from the window
start, for older buckets, as the public route's long windows do. Legacy outcome rows are
scoped by their gateway cluster owner, not by the stream tenant they also carry.

## Publication boundary

Bridge selects platform tenant scopes from `NETWORK_PUBLIC_TENANT_IDS`; a caller cannot
supply a tenant or request private data on the public route. Provisioning sets it to the
coordinator's tenant (the system tenant). Gateway observations carry their cluster owner
tenant, so that scope covers the coordinator's own inventory and prober results and the
platform clusters' gateways, never a tenant-owned cluster's. Public history includes
network facts, quotes, coarse errors and regional results. Customer/session identifiers,
manifest hashes, delivery correlation, detailed selection context, raw observer identities
and endpoint assignment internals are removed. Existing platform infrastructure map
visibility remains an independent policy and is not made public by this API.
Measurement site roles, regions, known coordinates and last-seen times are public with
stable opaque IDs so viewers can understand the provenance and coverage of measurements.

The GraphQL orchestrator inventory (`orchestratorsConnection`, `orchestratorInstances` and
the identity and instances of `orchestrator`) always includes the orchestrators the
`NETWORK_PUBLIC_TENANT_IDS` tenants' gateways discovered, for any caller, because it is
the same data this route publishes; it does not depend on the caller's access to private
clusters. Rows that name a gateway (`orchestratorVantages`, `orchestratorPerformanceSeries`
and the vantages of `orchestrator`) stay limited to gateway clusters in the public
topology or ones the caller owns or subscribes to.

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

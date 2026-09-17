# Multiregion Topology

FrameWorks runs one control plane and one set of durable data stores in an aggregator region, and a full media and realtime cell in every region. A viewer, publisher, or API client in a region is served by that region's cell; events and control mutations that need the durable stores travel to the aggregator. Production has two regions: `eu-west` (aggregator) and `us-east`.

## What runs where

| Component                                                                        | Placement           | Reason                                                                                                                                                                                              |
| -------------------------------------------------------------------------------- | ------------------- | --------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| Quartermaster, Commodore, Purser, Navigator, Periscope-Query, Periscope-Metering | Aggregator region   | They own or read the central databases and the aggregator Kafka. The CLI rejects placing Commodore, Purser, Periscope-Query, Periscope-Metering, or Periscope-Ingest outside the aggregator region. |
| Periscope-Ingest                                                                 | Aggregator region   | It projects every region's analytics and raw triggers into the central ClickHouse.                                                                                                                  |
| Kafka                                                                            | Every region        | Producers write locally; MirrorMaker2 replicates between regions. See [multiregion-kafka-mirrormaker.md](multiregion-kafka-mirrormaker.md).                                                         |
| Decklog                                                                          | Every region        | Media-plane producers use their own region's pool; central writers use the aggregator region's pool through `decklog.<aggregator region>.internal`.                                                 |
| Signalman                                                                        | Every region        | It serves local subscribers from local events plus mirrored copies of every other region's realtime events.                                                                                         |
| Foghorn                                                                          | One cell per region | Each cell has its own Redis (Sentinel in production) and its own Foghorn database. A Foghorn configured for Sentinel connects within a bounded retry or exits.                                      |
| Edges                                                                            | Any cluster         | An edge is controlled by one Foghorn cell at a time.                                                                                                                                                |

## Realtime delivery

Gateway routes GraphQL subscriptions to its own region's Signalman replicas. It opens one upstream Signalman stream per `(tenant, channel)`, shares it across every local subscriber of that key, gives each subscriber a bounded queue, and closes a subscriber that falls behind instead of blocking the others. Upstream streams run over a small pool of HTTP/2 connections per Signalman address.

Delivery to subscribers is at-least-once. Signalman suppresses most duplicates from the bare and mirrored topic copies with a bounded event-ID window, which does not survive a restart.

Signalman's broadcast consumers intentionally start at Kafka's latest offset
when their consumer group has no committed offset. Lag accounting uses the end
offset as that missing-commit baseline, so a new latest-reset consumer reports
zero lag rather than the topic's lifetime depth. Durable earliest-reset
consumers continue to use zero. Metrics for partitions no longer returned by
Kafka are deleted so removed topics and partitions cannot leave permanently
firing stale series.

## Durable events and billing

Quartermaster and Purser write service events into their own outboxes inside the transaction that changes state, and drain workers deliver them to Decklog; see [service-events.md](service-events.md). A cluster with no owner produces platform-scoped `cluster_created` and `cluster_updated` events without a tenant.

Raw Mist triggers from every region reach the aggregator through MirrorMaker2, where Periscope-Ingest projects final facts and metering. Usage in a region is billed once its trigger copy reaches the aggregator; Kafka retention bounds how long a regional outage can last before unreplicated triggers are lost.

## Private cluster ownership

A tenant-private cluster is controlled by one platform Foghorn cell, recorded as `control_cell_id`. Every running Foghorn of that cell serves the cluster, so losing one replica leaves its siblings serving it, and provisioning runs never remove those assignments. An operator moves a cluster to another cell with `frameworks admin clusters reassign-control-cell`; the previous cell releases the cluster's edges, they reconnect to the new cell, and the move completes once no live edge is observed by another cell. See [foghorn-ha.md](foghorn-ha.md) for the control-cell model and reassignment flow.

## Media placement

Which clusters may ingest or serve a stream is decided by media placement policy: tenant and stream rules compiled by Commodore into signed media authority and enforced by Foghorn at routing and final admission. See [media-authority.md](media-authority.md).

## Media DNS and gateway health

Quartermaster accepts only the canonical `edge-cap-{node}-{service_type}` row
when it compares and updates Foghorn-reported edge capability health. Historical
aggregate/instance rows do not participate in transition detection, so a full
snapshot cannot alternate between two rows for the same node and wake Navigator
on every interval.

Global Bunny entrypoints aggregate healthy nodes from active
`platform_official` clusters with one cluster-scoped inventory read per cluster.
Private-cluster candidates cannot authorize clearing a platform root record.

The Livepeer gateway binds its API to loopback and is exposed by the node's
`media_ingest` reverse proxy. Each running gateway keeps a stable physical name
at `livepeer-gateway.{node}.infra.{root}`; the proxy exposes only GET/HEAD
`/healthz` in addition to publish routes. Quartermaster probes that HTTPS URL.
Physical identity DNS is gated by desired ingress and active-node state, not by
the result of the probe that depends on that DNS record. Pooled `livepeer.*`
discovery remains gated on a fresh healthy result.

## Not built

- Per-cluster protocol allowlists do not exist; a cluster accepts every ingest protocol its edges support.
- A region with a single edge node has no media redundancy until more edges join its cluster.
- Periscope-Ingest has no regional ClickHouse; aggregate ingest runs only in the aggregator region.
- Storage-less private origins cannot yet write durable artifacts through another cell's storage.

## Growth triggers

- **A third region.** MirrorMaker2 links form a full mesh, so replication and Signalman consumption grow with the number of regions. Before adding one, move realtime replication to subscriber-aware forwarding or partition-aligned stream ownership; see [multiregion-kafka-mirrormaker.md](multiregion-kafka-mirrormaker.md).
- **Regional analytics.** Moving Periscope-Ingest out of the aggregator requires regional ClickHouse first.

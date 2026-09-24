# Multi-Region Kafka via MirrorMaker2

Every region runs its own KRaft Kafka cluster, and producers always write to the Decklog and Kafka in the region where an event happens. One cluster is the aggregator (`eu-west` in production): central Periscope-Ingest, ClickHouse and billing consume it. MirrorMaker2 (MM2) replicates between every pair of regions in both directions, with a different topic set per direction, so each region sees a complete realtime view and the aggregator sees a complete durable view.

This is the standard active-active Kafka shape: regional clusters take local writes, and remote regions receive source-prefixed copies (`{source_region}.{topic}`) that consumers subscribe to alongside the bare local topic.

## Topology

```
                   durable + realtime topics
[ us-east Kafka ] ────────────────────────────► [ eu-west Kafka (aggregator) ]
        ▲                                                     │
        └────────────────── realtime topics ─────────────────┘
```

With more regions the rule is the same for every ordered pair: links into the aggregator carry the durable set, and every other link carries only the realtime set.

## Topic sets

The canonical names and sets live in `pkg/topology/kafka_topics.go`; MM2 provisioning and every consumer read them from there.

| Direction                                        | Topics                                                                                                                              | Consumed by                                                  |
| ------------------------------------------------ | ----------------------------------------------------------------------------------------------------------------------------------- | ------------------------------------------------------------ |
| Regional to aggregator                           | `analytics_events`, `service_events`, `analytics.raw_mist_triggers`, `billing.usage_reports`, `decklog_events_dlq`, `domain.events` | Aggregator Periscope-Ingest and Purser; aggregator Signalman |
| Aggregator to regional, and regional to regional | `analytics_events`, `service_events`, `domain.events`                                                                               | Regional Signalman                                           |

`domain.events` carries the registered domain events ([service-events.md](service-events.md) §3.1). Its consumers dedup on `ce_id`, so the local topic and every `{source_region}.domain.events` copy can be read together. No service consumes it yet.

`analytics.raw_mist_triggers` is the raw final and accounting trigger journal; final facts and metering are projected from it, so regional usage is only billed once its copy reaches the aggregator.

Replication cannot loop or produce transitive names. Topic lists are full-name allowlists, so `analytics_events` never matches `us-east.analytics_events`; MM2's default replication policy skips topics that originated in the target; and every link also sets `topics.exclude` to `^(<every region>)\..*` alongside MM2's default internal-topic excludes.

## Operator manifest

`KafkaConfig.MirrorMaker` declares one link per direction. Each link's worker hosts must be in its target region.

```yaml
kafka:
  enabled: true
  region_id: eu-west
  role: aggregator
  brokers: [...]
  regional:
    - region_id: us-east
      role: regional
      brokers: [...]
  mirrormaker:
    enabled: true
    heap_opts: "-Xmx1G -Xms1G"
    task_count: 2
    links:
      - source: us-east
        target: eu-west
        hosts: [regional-eu-1, regional-eu-2, regional-eu-3]
      - source: eu-west
        target: us-east
        hosts: [regional-us-1, regional-us-2, regional-us-3]
```

The planner rejects a manifest that is missing a link for any ordered pair of Kafka regions, declares a link twice, names an unknown region, or places a worker outside the link's target region. A link may set `topics` or `task_count` to override the direction defaults.

## MirrorMaker2 workers

The CLI provisions MM2 as `kafka-mirrormaker` infrastructure tasks through the `frameworks.infra.kafka_mirrormaker` Ansible role, which runs `connect-mirror-maker.sh` under systemd as `frameworks-kafka-mirrormaker`. There is one worker process per host, started with `--clusters <host region>`, so it drives every link into its own region and nothing else. Workers serving the same target coordinate task ownership through MM2's Connect internals, and replicated writes stay local to the cluster they land in. Consumer-group checkpoints are emitted only on links into the aggregator, where durable consumer groups live.

Workers run with `dedicated.mode.enable.internode.rest` (KIP-710, Kafka 3.5+). When a follower worker recomputes a connector's tasks, for example after a new source topic appears, it forwards the task configs to the leader over this REST server; without it the forward fails and the new topic never gets a replication task while its remote `<source>.<topic>` copy stays empty. The listener binds and advertises the host's WireGuard mesh IP on the worker port (8083), so it is reachable only from other mesh hosts. Source topics and consumer groups are rescanned every 60 seconds. `frameworks cluster diagnose kafka` compares each mirrored topic's source end offset with its remote copy and warns on a topic that has records on the source but none on the target.

`cluster release apply` converges every worker's config and binaries first, with the role only recording a pending restart (`/var/lib/kafka-mirrormaker/restart-pending`), and then restarts all pending workers together. Workers of one target therefore never run mixed configs across a sequence of rebalances. The marker lives on the host, so a rerun after an interrupted release still restarts a worker whose config changed earlier. `cluster provision` restarts each worker as its config changes.

## Consumers

The CLI renders `MIRROR_REGION_PREFIXES` for Periscope-Ingest and Signalman from the links whose target is the Kafka cluster the service binds.

- **Periscope-Ingest** binds the aggregator. It consumes the bare and every prefixed copy of `analytics_events`, `service_events` and `analytics.raw_mist_triggers`. Raw triggers use a retry-only handler so a ClickHouse outage never commits past an unprojected final. Handlers are idempotent on event and source-request identity, so mirrored duplicates converge.
- **Signalman** binds its own region's cluster and consumes the bare and every prefixed copy of `analytics_events` and `service_events`. Each replica uses its own consumer group with `latest` reset. A bounded event-ID window suppresses common duplicates; delivery to subscribers is at-least-once, not exactly-once, because the window does not survive restarts or eviction.

Gateway routes every GraphQL subscription to the Signalman replicas of its own region; each regional Signalman already holds local events and every other region's realtime events.

## Volume envelope

Each Signalman replica parses its region's realtime events plus every other region's, so its consumed rate grows with `regions x replicas`. Cross-region replication egress on a link equals the source region's realtime event rate. When a third region lands or that rate becomes a measured cost, move to subscriber-aware forwarding (replicate a stream's events only to regions with live subscribers) or partition-aligned stream ownership instead of adding regions to the mesh.

## Decklog locality

Decklog reads `KAFKA_BROKERS` at startup, and the CLI renders it from the Decklog host's region. Media-plane producers (Foghorn, Bridge, Livepeer Gateway) resolve `decklog.internal` to their own region's Decklog pool, so their events enter local Kafka.

Central control writers (Commodore, Purser, Quartermaster, Deckhand, Skipper) declare their Decklog dependency with the `aggregator_region` DNS scope in `pkg/topology/dependencies.go`. The CLI renders `DECKLOG_GRPC_ADDR=decklog.<aggregator region>.internal` for them, and Privateer publishes that record with only the aggregator region's Decklog replicas, so their events enter the aggregator Kafka directly instead of crossing to another region and returning through MirrorMaker2. Decklog clients dial with `round_robin`, spreading writers across the pool.

Services that bind the aggregator Kafka (Periscope-Ingest, Periscope-Metering, Periscope-Query, Purser, Commodore) must be placed in the aggregator region; the CLI rejects any other placement.

## Decklog outage verification

State-coupled events wait in their producer's service-side outbox while Decklog is unavailable, then drain when it returns; loss-tolerant telemetry is fire-and-forget. To verify a regional outage:

1. Confirm both regional KRaft clusters and the MM2 workers for each link are running.
2. Confirm the US Decklog points at US Kafka.
3. Stop the US Decklog process and drive ingest traffic; confirm producer outboxes grow.
4. Restart the US Decklog and confirm the outboxes drain.
5. Confirm aggregator Periscope-Ingest receives the `us-east.` copies with no duplicate rows in ClickHouse, and that EU and US Signalman subscribers each see the events once.

# Multi-Region Kafka via MirrorMaker2

The EU Kafka cluster doubles as the aggregator. Each additional region runs its own KRaft cluster. MirrorMaker2 (MM2) mirrors a small canonical set of topics from every non-aggregator regional cluster onto the aggregator.

## Topology

```
[ regional-eu Kafka ]  ◄──────── mirror eu.* topics ──── consumed by central Periscope-Ingest
   (also = aggregator)
       ▲                                                    ▲
       │ MirrorMaker2                                        │ consumes both
       │                                                     │   bare regional topics
[ regional-us Kafka ]  ──────── mirror us.* topics ─────────┘   plus mirrored {source}.* topics
   (Ashburn, KRaft single-broker today; scales to 3 brokers when load justifies it)
```

## Topics mirrored

The canonical set MirrorMaker2 mirrors out of each regional cluster (named without prefix on the source, prefixed `{region_id}.` on the aggregator per MM2 default):

| Source name             | Aggregator name                                                          | Purpose                                                                         |
| ----------------------- | ------------------------------------------------------------------------ | ------------------------------------------------------------------------------- |
| `analytics_events`      | `us.analytics_events` (and `eu.analytics_events` if EU is also a source) | Stream lifecycle, viewer events, routing decisions                              |
| `service_events`        | `{region}.service_events`                                                | Service-level events from Bridge / Commodore / Quartermaster / Purser / Foghorn |
| `decklog_events_dlq`    | `{region}.decklog_events_dlq`                                            | Decklog DLQ for failed ingest; aggregated for cross-region replay/inspection    |
| `billing.usage_reports` | `{region}.billing.usage_reports`                                         | Billing usage from Periscope-Query; aggregator-side consumer is Purser          |

Non-mirrored topics stay regional (e.g. stream-scoped live realtime topics — Signalman regional consumes locally; no default global mirroring).

## Operator manifest

In `cli/pkg/inventory/types.go`, `KafkaConfig.Regional []RegionalKafkaCluster` carries the non-aggregator regions and `KafkaConfig.MirrorMaker` declares the worker. The primary `KafkaConfig` is the EU cluster + aggregator role.

```yaml
kafka:
  enabled: true
  cluster_id: <EU KRaft UUID>
  controllers: [...]
  brokers: [...]
  topics: [...]
  regional:
    - region_id: us-east
      role: regional
      cluster_id: <US KRaft UUID generated via kafka-storage.sh random-uuid>
      controllers: [...]
      brokers:
        - { host: regional-us-1, id: 11, port: 9092 }
      topics:
        - { name: analytics_events, partitions: 6, replication_factor: 1 }
        - { name: service_events, partitions: 3, replication_factor: 1 }
        - { name: decklog_events_dlq, partitions: 3, replication_factor: 1 }
        - { name: billing.usage_reports, partitions: 3, replication_factor: 1 }
      mirror_topics: [] # empty = canonical set above
  mirrormaker:
    enabled: true
    host: regional-eu-1
    heap_opts: "-Xmx1G -Xms1G"
    replicas: 3
    task_count: 2
```

## MirrorMaker2 workers

The CLI provisions MM2 as `kafka-mirrormaker` infrastructure tasks (Ansible role `frameworks.infra.kafka_mirrormaker`). The role installs Kafka's standard tarball alongside the broker/controller install, renders `mm2.properties`, and runs `connect-mirror-maker.sh` under systemd as `frameworks-kafka-mirrormaker`. The source clusters and aggregator target come from `KafkaConfig.Regional` plus `KafkaConfig.MirrorMaker.Hosts` (`Host` remains accepted for single-worker manifests). MM2 workers must run in the aggregator Kafka region; the planner rejects worker hosts in other regions. MM2's dedicated mode supports multiple worker processes using the same config. MM2's default replication policy adds the source-cluster alias (the region_id) as the topic prefix.

## Periscope-Ingest and Signalman consumption

Periscope-Ingest is pinned to the aggregator Kafka cluster while ClickHouse is central. Every Periscope-Ingest replica, even when the process is placed on a non-aggregator host for failure tolerance, consumes the same bare aggregator topics and source-prefixed mirror topics:

- Bare topics: `analytics_events`, `service_events`, `decklog_events_dlq`, `billing.usage_reports` — events written directly to the aggregator-region Kafka.
- Mirror topics: `us.analytics_events`, `us.service_events`, etc. — MM2-mirrored events from non-aggregator regions.

The CLI emits `MIRROR_REGION_PREFIXES` automatically for every Periscope-Ingest instance, listing every non-aggregator `region_id`. Each consumer registers an additional handler per prefix. Signalman stays region-local and does not consume mirrored topics; Gateway routes stream-scoped subscriptions to the stream origin region.

Idempotency: the envelope v2 fields (`event_id` UUIDv7, `source_region`, `source_cluster_id`) on every event allow `ReplacingMergeTree(event_id)` in ClickHouse and ingest-side duplicate checks. Mirroring is at-least-once; dedup at consume time makes it effectively-once.

## Decklog locality contract

Decklog reads `KAFKA_BROKERS` env at startup. Regional Decklog (on `regional-us-1`) points at the US Kafka brokers; the aggregator-region Decklog (on `regional-eu-*`) points at EU brokers. Producers (Foghorn, Bridge, Commodore, etc.) dial the **regional** Decklog via `DECKLOG_GRPC_ADDR` set in their gitops env per cell.

No new code in Decklog itself. The `KAFKA_BROKERS` env scopes the broker list naturally.

## Verification on Decklog outage

The shared `pkg/outbox` plus the per-producer outbox migration ensure state-coupled events (Commodore stream/policy invalidation, Purser billing/plan changes, Quartermaster cluster events, Foghorn artifact lifecycle) sit in their service-side outbox tables, not in-flight to Decklog. A regional Decklog outage means the outbox grows; the drain worker catches up after Decklog returns. Loss-tolerant telemetry (Bridge API usage, Foghorn load-balancing telemetry) stays fire-and-forget and accepts loss during outage windows.

Verification runbook:

1. Confirm the US KRaft cluster and MM2 worker are up.
2. Confirm regional US Decklog points at the US Kafka.
3. Stop the US Decklog process.
4. Drive ingest traffic; observe Foghorn outbox grows.
5. Restart US Decklog.
6. Observe outbox drains; Periscope-Ingest receives the mirrored events from aggregator Kafka with `event_id` dedup (no duplicates in ClickHouse).

## EU as aggregator

EU Kafka serves a dual role: EU regional events land here directly; US-region mirror lands here under the `us-east.` prefix. Periscope-Ingest consumes both bare EU topics and prefixed US mirror topics from aggregator Kafka. When a third region is added, the operator either keeps the aggregator on EU or moves it to a dedicated cluster by marking that cluster's `role: aggregator` in `KafkaConfig.Regional`.

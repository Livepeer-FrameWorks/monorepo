# RFC: Foghorn HA state-sync ordering invariant

Status: Draft — design gate before implementing the B1–B4 / L3 fixes
Owner: (assign)
Related: `PLAN_*` HA audit; acceptance specs in
`api_balancing/internal/control/ha_ordering_spec_test.go` and
`api_balancing/internal/state/ha_ordering_spec_test.go` (currently `t.Skip`).

## Problem

Foghorn replicates two caches across HA replicas over **Redis pub/sub**, which
guarantees neither cross-publisher ordering nor replay:

1. **Stream registry** (`internal/control/stream_registry_redis_sync.go`) —
   per-Location source state + artifacts. Has a partial guard that compares
   **cross-machine wall clocks** (`maxLocationUpdatedAt(local).UnixNano()` vs the
   delete's `PublishedAtUnixNano`; artifact guard vs local `cached` time).
2. **Node/stream state** (`internal/state/cache.go` `applyRedisChange`) —
   node/stream/instance state. **Wholesale-replaces and deletes
   unconditionally**; `StateChange` (`internal/state/redis_store.go:70-78`) has
   **no ordering field at all**.

Confirmed defects (code-verified):

- **B1** registry guards are wall-clock based → wrong under clock skew.
- **B2** a stale peer snapshot rolls back fresher local `IsHealthy`/score.
- **B3** a stale peer node-delete evicts a fresher local node.
- **B4** post-restart, rehydrate stamps `cached=now`, so a valid pre-restart
  delete is dropped (stale leak).
- **L3** rehydrate completes before the subscription arms → startup lost-update.

Root cause: ordering by wall-clock (or nothing). The fix is a **monotonic logical
version**, NOT a quorum system (etcd/Consul) — that would re-impose the
self-host operational weight the platform deliberately avoids.

**Constraint (do not skip):** a single per-snapshot `Version` is insufficient
until the writer model is explicit. The node state is published as a _whole-node
snapshot_ by many narrow setters; a naive snapshot version makes unrelated field
updates clobber each other deterministically rather than fixing it.

## Writer-model enumeration (the part that must be pinned first)

### Stream registry — per **Location**, single authoritative writer

A `StreamEntry.Locations` map is keyed by `cluster_id`; each Location is owned by
its cluster's authoritative Foghorn. Source upserts today carry **no**
`PublishedAtUnixNano` (`stream_registry_redis_sync.go:329`) and ride
`Location.UpdatedAt`. → A **per-Location version** is the natural shape; no
multi-writer ambiguity because one cluster owns each Location.

### Node state — mostly single-owner, with two cross-cutting exceptions

Every node mutation funnels through `persistNodeWriteThrough(nodeID,
nodePayloadLocked(nodeID))` — i.e. a full-node snapshot. Writers:

| Writer                                                                                             | Source                  | Owner?                        |
| -------------------------------------------------------------------------------------------------- | ----------------------- | ----------------------------- |
| `TouchNode`, `MarkNodeDisconnected`, `SetProbeVerified`                                            | node heartbeat/liveness | owner (control-stream holder) |
| `SetNodeInfo`, `UpdateNodeMetrics`, `UpdateNodeDiskUsage`, `SetNodeGPUInfo`, `SetNodeStoragePaths` | node telemetry ingest   | owner                         |
| `SetNodeOperationalMode`                                                                           | operator / API          | **any instance**              |
| `UpdateUserConnection` → viewer-bandwidth penalty                                                  | viewer routing          | **any instance**              |

So a node is _almost_ single-writer (the Foghorn holding its control stream), but
operational-mode and viewer-bandwidth writes can originate on any replica. This
is exactly why a top-level snapshot version is unsafe.

## Candidate mechanisms

### Registry sources (recommended): per-Location version

Scope: **source** entries only (`RegistryEntitySource`). Add a monotonic
`Version uint64` to `Location`, incremented by the owning cluster on each
mutation; stamp it on `RegistryChange` for both source upsert and source delete
(note source upserts carry no `PublishedAtUnixNano` today — `:329`). Apply rule:
accept a Location, or honor a source delete, iff `incoming.Version >
local.Version`. Replaces the source wall-clock compare at `:185` and the
`mergeStreamEntry` `UpdatedAt` compare at `:167`. Fixes **B1**. This does NOT
cover artifacts — see below.

### Registry artifacts: per-artifact version + tombstone

`ArtifactEntry` is **not** a `Location` (no per-cluster map, no `UpdatedAt`); the
current guards at `:233/:254` compare the peer's publish time against the local
`cached` wall-time, which is exactly what breaks after restart (B4). Artifacts
need their own ordering:

- Give `ArtifactEntry` a monotonic `Version uint64` (owner-stamped, or the same
  Redis-`INCR` source chosen for state below — artifacts are written by the
  origin cluster, so a single authoritative writer per artifact hash is the
  common case). Stamp it on the artifact `RegistryChange` for upsert and delete.
- Apply rule: accept an artifact upsert/delete iff `incoming.Version >
local.Version`, replacing the `cached`-time compares at `:233/:254`.
- **Tombstone:** retain the version of a deleted artifact hash (short TTL) so a
  late, lower-version upsert can't resurrect it.
- Rehydrate must preserve each artifact's stored `Version` instead of stamping
  `cached=now`. This is what actually fixes **B4** (the `TestApplyRedisChange_PostRestartDelete_StillApplies` spec).

### Node/stream state — two options, pick in review

- **Option A — per-entity owner version.** Authoritative owner increments a
  per-node version; non-owner writes (operational-mode, viewer-bandwidth) are
  **routed to the owner** or **split into their own keyed sub-records** so they
  never publish a full-node snapshot. Correct, but needs a routing/ownership
  change.
- **Option B — Redis server-assigned monotonic version (recommended).** Use a
  Redis `INCR` (or stream sequence) per entity key as the version source. Redis
  is already the single bus, so the version is globally monotonic and
  **writer-agnostic** — it sidesteps the multi-writer problem entirely without an
  ownership refactor. Cost: one extra round-trip per write-through (acceptable;
  writes already do `SET`). `StateChange` gains a `Version uint64`; the apply
  path guards every replace and every delete on `incoming.Version >
local.Version`.

**Rejected:** a broad top-level Lamport stamp applied uniformly to whole-node
snapshots — the enumeration shows node writes are not single-writer, so it would
deterministically clobber cross-cutting fields.

## L3 — startup ordering

In both `EnableRedisSync` paths, arm the subscription **before** rehydrating and
feed the rehydrate snapshot through the (now version-guarded) apply path, so a
message that races in during startup is ordered rather than lost.

## Acceptance criteria

Un-skip and make green (currently `t.Skip`):

- `control`: `TestApplyRedisChange_SkewedDelete_DoesNotWipeFresherSource` (B1),
  `TestApplyRedisChange_PostRestartDelete_StillApplies` (B4).
- `state`: `TestApplyRedisChange_StaleSnapshot_DoesNotRollBackHealth` (B2),
  `TestApplyRedisChange_StaleDelete_DoesNotEvictFresherNode` (B3).

## Out of scope

L4 leader-lease split-brain — low blast radius (origin pull deduped by the
durable mark; other leader actions idempotent). Revisit with a lease epoch only
if evidence warrants.

## Decision

- [ ] Registry sources: per-Location version — approved?
- [ ] Registry artifacts: per-artifact version + tombstone — approved?
- [ ] State: Option A (owner version) vs Option B (Redis-sequenced) — pick one.
- [ ] L3 subscribe-before-rehydrate — approved?

On sign-off, implement in `internal/control/stream_registry_redis_sync.go`,
`internal/state/{cache,redis_store,stream_state}.go`, un-skip the specs, then fold
this RFC's conclusion into `docs/architecture/` and delete this file.

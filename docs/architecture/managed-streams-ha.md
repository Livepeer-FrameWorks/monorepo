# Managed streams under multi-Foghorn HA

`mist_native` streams reconcile through Foghorn's managed-stream reconciler.

Public stream reads project this internal mode as GraphQL `MANAGED`. They expose a
`ManagedSourceView` containing only `source_kind`, `always_on`, and `placement_count`; the
literal source specification is never returned. Where the source may run is read from
`Stream.sourceLocation`, which bootstrap declares and `updateStream` refuses to change for a
managed stream. Managed streams do not
accept publisher credentials: GraphQL returns a null `streamKey`, and Commodore rejects primary or
secondary stream-key management for every non-push mode.
The implementation uses Foghorn's existing HA primitives — Redis-backed
cluster-wide node state and the command-relay forwarding service — so
multi-Foghorn-per-cluster deployments place streams correctly without any
operating constraint.

## Cluster-wide placement + ownership

`mist_native` source placement is cluster-local. A managed stream's bootstrap
`source_location` names exactly one source cluster, optionally narrowed to
nodes of that cluster and with avoided nodes. Bootstrap writes it as the
stream's own ingest placement rules and mirrors the cluster into
`stream_mist_sources.allowed_cluster_ids`, which the signed source definition
still carries until the pin columns are retired (see
[media placement](media-placement-policy.md#pull-source-pin-conversion-and-retirement)).
`eligibleNodesAcrossClusters` returns every healthy non-stale
`(node_id, cluster_id)` pair from the Redis-backed node state in that cluster.
The reconciler then drops every node the signed ingest policy denies for that
exact node; a node whose verdict lacks facts makes the tick transient for the
stream instead of shrinking the pool. `placementPickWithCluster` runs a
deterministic stable-hash on `stream_id` against the remaining node set, so
every Foghorn in the cluster computes the same elected pair. Materialization
and `ApplyManagedStream` dispatch check the ingest policy again for the elected
node.

Viewer routing is still cross-cluster: once the source is placed, Foghorn
records `active_ingest_cluster_id`. Policy may route a viewer back to that
source cluster or choose another entitled edge; the latter prepares one
receipt-bound DTSC relay from the elected origin rather than asking the remote
edge to interpret or recreate the Mist-native source specification.

**Ownership** has two layers:

1. **Cluster ownership**: each reconciler tick acts on a stream only
   when the elected node's cluster equals the tick's own cluster. This
   is defensive bookkeeping; current `mist_native` rows have exactly one
   allowed source cluster.
2. **Connection ownership**: within the same cluster, peer Foghorn
   instances may each see the same election but only one holds the
   Helmsman bidi stream for the elected node. The reconciler asks
   Redis (`GetConnOwner`) and acts only when this instance owns the
   connection. Non-owners skip — without this filter, the non-owner
   would relay `ApplyManagedStream` every tick (sidecar idempotency
   makes it non-destructive, but the non-owner never receives the
   node's Heartbeats so `verifiedApplied` stays empty, the
   `snapshotStable && !verifiedApplied` re-emit gate keeps firing, and
   the relay channel floods). Single-Foghorn deployments fall through
   to the local registry, which always reports ownership when the node
   is connected.

When the Redis store is not configured (single-Foghorn topology or
local-only development), the function falls back to the local registry.
Placement remains correct because there is only one registry.

## Cross-Foghorn Apply/Retract dispatch

`SendApplyManagedStream` / `SendRetractManagedStream` follow the
`SendClipPull` / `SendDVRStart` pattern:

1. Try the local registry (`SendLocalApplyManagedStream`).
2. On `shouldRelay(nodeID, err)` (typically `ErrNotConnected`), forward
   via `commandRelay` to the peer Foghorn that owns the node's stream.
3. Peer's `RelayServer.ForwardCommand` receives the
   `ForwardCommandRequest_ApplyManagedStream` or
   `_RetractManagedStream` and dispatches to its own
   `SendLocalApplyManagedStream` / `SendLocalRetractManagedStream`.

The relay's owner lookup uses `RedisStateStore.GetConnOwner`, which every
Foghorn writes on Helmsman register.

## State recovery semantics

- **Foghorn restart**: `Register` carries the sidecar's
  `applied_managed_streams` (with `stream_id` parsed from
  `fw:stream:<id>` Mist tags). The handler routes through
  `HydrateManagedStreamLastSentForNode` which lands entries in a
  pending-cluster bucket; the next reconciler tick migrates them into
  the right cluster bucket via `adoptHydratedManagedStreams`.
- **Helmsman disconnect**: `ForgetManagedStreamLastSent(nodeID)` drops
  cached Apply state so reconnect re-emits Apply.
- **`active_ingest_cluster_id`**: recorded only after the sidecar's
  Heartbeat-borne `applied_managed_streams` snapshot confirms the stream
  is in Mist's config (verifiedApplied gate). Apply/Retract on the
  control channel are fire-and-forget at the protocol layer; the
  Heartbeat snapshot is the ground truth that closes the loop. On every
  subsequent reconciler tick where the verified set still confirms,
  the same idempotent UPDATE re-converges routing (same 30s
  contended-update guard PUSH_REWRITE uses) without re-emitting Apply.
  A wire-send-succeeds-but-Mist-rejects situation surfaces on the next
  Heartbeat as "stream missing from sidecar's applied set" → the
  reconciler re-emits Apply rather than pinning routing at the wrong
  cluster.

## Failure modes

- **Redis store configured and unreachable**: when `GetAllNodes` /
  `GetConnOwner` fail, the placement and ownership helpers fail closed
  with transient status instead of reading from the local registry. The
  reconciler tick skips placement for that stream and suppresses retracts
  for previously-applied state on the elected node. Next tick retries
  every 30 s.

  This is deliberate: falling back to the local registry in HA would
  let two peer Foghorns elect against their partial connection views,
  apply the same single-active stream on different nodes, and lose the
  `CapEdge`/`IsStale` filters the registry does not carry. The
  worst-case behavior of "skip until Redis recovers" is preferable to
  "potentially double-place during the outage."

- **Relay dial failure**: `commandRelay.forward` returns; `relayFailure`
  wraps the original local error and the reconciler records the Apply
  failure. Next tick re-tries.
- **Split brain (Redis owner stale)**: `commandRelay` enforces a
  challenge-response via the connection owner record; a stale owner
  entry resolves on the next Helmsman register.

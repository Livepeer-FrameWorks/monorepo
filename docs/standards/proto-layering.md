# Proto layering

`pkg/proto` is layered so that a change to one service's contract does not force
every other service to rebuild. The release planner hashes the exact `.proto`
closure a binary compiles (see [build-and-packaging](../architecture/build-and-packaging.md)),
so a tangled proto graph directly inflates carry-forward misses.

## The rule

A `.proto` may import another monorepo `.proto` only **downward**:

| Layer                       | Files                                                                                                                                                   | May be imported by               |
| --------------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------- | -------------------------------- |
| 0 - value types             | `common.proto`, `cluster_peer.proto`, `tenant_limits.proto`, `metering_contract.proto`, `x402.proto`                                                    | anyone                           |
| 1 - cross-service contracts | `shared.proto` (clip/DVR/VOD/playback), `foghorn_control.proto` (the Foghorn control msgs Commodore proxies)                                            | any Layer-2 proto                |
| 2 - service / event APIs    | `commodore`, `quartermaster`, `purser`, `foghorn`, `periscope`, `signalman`, `ipc`, `deckhand`, `skipper`, `dns`, `foghorn_federation`, `foghorn_relay` | only their own service's Go code |

**A Layer-2 service proto must not import another Layer-2 service proto.** If two
services need the same type, the type belongs in a Layer-0/1 package, not in one
service's API.

### The only allowed exceptions

Genuine consumers of the published IPC media-plane event contract:

- `periscope -> ipc`, `signalman -> ipc` - analytics/realtime consume IPC events.
- `foghorn_relay -> ipc` - wraps IPC commands for HA forwarding.

These are listed in `allowedServiceEdges` in `tools/release-plan/proto_layering_test.go`.
Adding a new cross-service edge means adding an allowlist entry **deliberately**;
that friction is the point.

## Why these packages exist

They were carved out to break edges that fanned rebuilds across the platform:

- `cluster_peer` / `tenant_limits` / `metering_contract` removed
  `commodore -> quartermaster`, `commodore -> purser`, `purser -> quartermaster`
  (all were value-type bleed: `TenantClusterPeer`, `TenantResourceLimits`,
  `MeterAllowance`).
- `x402` removed `commodore -> purser` for the payment payload (a contract shared
  by Gateway, Commodore, and Purser).
- `foghorn_control` removed `commodore -> foghorn` for the DVR-chapter / node /
  tenant control messages Commodore proxies. The gRPC **services** stay in
  `foghorn.proto`, so method paths are unchanged.
- `ipc` itself is no longer dragged into bootstrap-only services:
  `quartermaster.proto`'s `EnqueueServiceEventRequest.event` is `bytes` (a binary-
  marshaled `ServiceEvent`), not the typed message, so `quartermasterpb` doesn't
  compile `ipcpb`. The producer-side marshal lives in
  `pkg/clients/quartermaster/events`. On the **binary** wire a length-delimited
  message and a `bytes` field are identical, so this is interchangeable with the
  typed field over gRPC; but **not** over ProtoJSON (message-to-bytes is unsafe in
  JSON), which this path never uses.

## gRPC client wrappers

The same downward rule applies to `pkg/clients/<svc>`: a client wrapper should
import only the proto packages its method signatures use. Federation/cross-cluster
stubs are split into `pkg/clients/foghorn/federation` (`foghornfed`) so that
Commodore and the Gateway don't compile `foghorn_federation` just by importing the
base Foghorn client. (`Relay()` stays on the base client because the relay layer's
`CommandRelayClient` interface is a testing seam built on it.)

## Wire compatibility when moving a type

Moving a message between `.proto` files keeps its wire format (field numbers) but
changes its fully-qualified name. Safe because FrameWorks gRPC messages are matched
by field number, not type name; but **preserve every field number**, never move a
`service`/RPC (method paths are `/<package>.<Service>/<Method>`), and check the type
isn't packed into a `google.protobuf.Any` (type URLs embed the FQN).

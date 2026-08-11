# RFC: NAT Traversal as a Platform Capability

## Status

Draft

## TL;DR

- Go Foghorn should inherit MistServer's C++ Foghorn NAT traversal capabilities.
- NAT type classification becomes a scoring signal in viewer routing.
- Hole punching coordination becomes a platform-managed feature.
- Nautophone monitoring gets absorbed into operator dashboards.
- The plane split: the Go control plane ADMINISTERS classification and ORCHESTRATES punches; the
  C++ media plane EXECUTES them.

## Current State

Upstream MistServer's C++ Foghorn (`lib/foghorn.cpp`, `src/utils/util_foghorn.cpp`) implements a complete NAT hole punching coordination system: SHA256-authenticated UDP packets, NAT type classification (OPEN, CONSISTENT, PREDICTABLE, IMPENETRABLE), multi-attempt punching with port randomization, and background punch threads. MistServer's STUN library (`lib/stun.cpp`) implements the full STUN protocol. `MistUtilFoghorn` runs as a standalone coordination server on port 7077.

Those MistServer source files are upstream/external context, not files currently present in this monorepo. The repository currently carries MistServer deployment roles, not the C++ source tree.

Nautophone (`mistserver/nautophone/`) provides a web UI and Node.js translator service for monitoring endpoint states.

Go Foghorn (`api_balancing/`) replaced C++ MistUtilLoad-style load-balancing responsibilities in the platform, but it has NOT absorbed C++ Foghorn's NAT traversal capabilities. The two Foghorns serve different purposes today.

Inter-server network performance is not measured anywhere in the platform today: no component measures latency or throughput between platform servers. Node telemetry (CPU, RAM, bandwidth counters) is per-node, and the geo distance used in balancer scoring is a static proxy, not a measurement of the path.

SDKs partially support ICE server configuration: `npm_studio` WhipClient accepts optional `iceServers` in its config (`types.ts`). `npm_player` NativePlayer and MistWebRTCPlayer cast `iceServers` from the source via `as any`; it is NOT in the public `StreamSource` interface.

There is no platform-managed TURN/STUN infrastructure and no repo-managed `MistUtilFoghorn` deployment path. Operators would need to manage that external binary/process themselves today.

Evidence:

- Upstream MistServer `lib/foghorn.cpp`
- Upstream MistServer `lib/foghorn.h`
- Upstream MistServer `lib/stun.cpp`
- Upstream MistServer `src/utils/util_foghorn.cpp`
- Upstream MistServer `nautophone/`
- `ansible/collections/ansible_collections/frameworks/infra/roles/mistserver/`
- `npm_player/packages/core/src/core/PlayerInterface.ts`
- `npm_studio/packages/core/src/types.ts`
- `api_balancing/internal/balancer/balancer.go`

## Problem / Motivation

WebRTC playback (WHEP) and publishing (WHIP) fail behind symmetric NATs and restrictive firewalls. The C++ Foghorn already solves this at the MistServer level, but it is invisible to the platform layer. Operators get no NAT state visibility in their dashboards, the load balancer cannot make NAT-aware routing decisions, and there is no automated fallback to relay when hole punching fails.

## Goals

- Surface NAT type as a first-class edge attribute in Foghorn's scoring model.
- Make hole punching coordination a platform-managed capability (not a manually deployed binary).
- Enable NAT-aware viewer routing (prefer OPEN edges for WebRTC traffic).
- Provide relay fallback for impenetrable NATs.

## Non-Goals

- Replacing MistServer's C++ STUN/ICE implementation for WebRTC media path negotiation.
- Building a full TURN relay server from scratch — MistServer edges with OPEN NAT serve as natural relays.
- Supporting non-WebRTC protocols through hole punching.

## Proposal

The division of labor across all phases: the **Go control plane** owns classification
ADMINISTRATION (keeping every node's NAT type current — collection via Helmsman, periodic
re-classification, staleness handling), punch ORCHESTRATION (deciding whether, when, and between
whom a punch happens), and the placement-signal integration (feeding NAT type into balancer scoring
and into the placement policy engine's `serve`/`process` decision inputs — see
`docs/rfcs/placement-policy-engine.md`). The **C++ media plane** EXECUTES the punch: MistServer's
STUN/hole-punch machinery sends the actual packets and holds the resulting paths. The Go side never
touches media-path packets; the C++ side never decides routing.

### Phase 1: NAT type as scoring signal

Foghorn already scores edges by CPU, RAM, bandwidth, and geo distance. Add NAT type as a new scoring dimension. OPEN edges receive a bonus for WebRTC viewers; IMPENETRABLE edges get deprioritized. Edge nodes report their NAT type via Helmsman (MistServer already classifies this internally — Helmsman relays the classification, keeping administration in the control plane while classification measurement stays in the media plane). New field in the EdgeTelemetry protobuf message. The same per-node NAT type doubles as a decision input at the placement policy engine's `serve`/`process` verbs.

### Phase 2: Built-in coordination server

Port C++ MistUtilFoghorn coordination logic into Go Foghorn. Add the Foghorn UDP protocol on port 7077 alongside existing gRPC (18019) and HTTP (18008). The C++ implementation is approximately 660 lines — well-scoped for a port.

### Phase 3: Platform-managed hole punching

When Foghorn routes a viewer to an edge, if both sides have compatible NAT types (e.g., CONSISTENT to CONSISTENT), Foghorn orchestrates the punch automatically — the MistServer processes on each side execute it. If both sides are IMPENETRABLE, Foghorn routes through an OPEN edge as a relay instead.

#### Inter-server network performance measurement

The punch/probe machinery gives the platform, for the first time, active probes between its own servers. This phase extends those probes into a measurement capability: coordination exchanges already yield round-trip timings, and scheduled probe runs between server pairs add throughput estimation on top. Inter-server latency and throughput are measured nowhere in the platform today (see Current State); this subsection makes them a structured signal with two consumers:

- **Balancer scoring** — relay selection (which OPEN edge relays an IMPENETRABLE pair) and inter-server routing generally can prefer measured-good paths over the static geo-distance proxy.
- **The workload/capacity model** — measured network performance joins measured utilization as a placement input; see `docs/rfcs/workload-cost-model.md`, whose continuous-adjustment consumer expects exactly this signal.

How aggressively to probe is a research question in its own right: throughput probing consumes the bandwidth it measures, so probe cadence and sizing must stay subordinate to live traffic. The signal ships as observability first; consumers opt in separately.

### Phase 4: ICE server injection

Foghorn injects `iceServers` into balancer responses with short-lived TURN credentials when relay is needed. Add `iceServers` to the `StreamSource` interface in `npm_player`. SDKs auto-consume platform-provided ICE servers without operator configuration.

## Owning services / modules

- **Foghorn (api_balancing, control plane)** — classification administration (per-node NAT-type
  registry, staleness/re-classification policy), punch orchestration (UDP coordination server,
  punch/relay decisions), NAT-aware scoring, ICE credential injection, the placement-signal feed
  into the policy engine, and inter-server network-performance measurement (probe scheduling,
  per-pair latency/throughput registry) feeding balancer scoring and the workload cost model
  (`docs/rfcs/workload-cost-model.md`).
- **Helmsman (api_sidecar, media plane edge)** — relays MistServer's NAT classification to Foghorn
  via EdgeTelemetry.
- **MistServer (C++ media plane)** — measures NAT type and executes punches (STUN, port
  randomization, punch threads); OPEN edges double as relays.
- **pkg/proto** — EdgeTelemetry NAT-type field, coordination messages.
- **npm_player / npm_studio** — consume injected `iceServers`.
- **Chartroom/Foredeck** — absorbed Nautophone monitoring surface.

## Impact / Dependencies

- `api_balancing` — scoring model, new handlers, UDP listener.
- `pkg/proto` — EdgeTelemetry message extension for NAT type.
- `api_sidecar` — Helmsman reports NAT type from MistServer to Foghorn.
- `npm_player` — `StreamSource` interface gains `iceServers` field.
- `npm_studio` — auto-consume platform-provided ICE servers (already partially supported).
- Nautophone monitoring capabilities absorbed into Chartroom/Foredeck dashboards.

## Alternatives Considered

- **Deploy coturn alongside edges.** Adds another binary to manage. Does not leverage MistServer's existing NAT capabilities. Operators already run MistServer — adding coturn increases operational surface.
- **Keep MistUtilFoghorn as a separate binary.** Operators must deploy it manually. NAT state remains invisible to the platform. No NAT-aware routing.
- **Rely on browser-default STUN only.** Fails behind symmetric NATs. No fallback mechanism. The problem this RFC addresses remains unsolved.

## Risks & Mitigations

- **UDP coordination protocol is security-sensitive.** The C++ implementation uses SHA256 authentication. The Go port must preserve this. Mitigation: direct port of the auth logic with test vectors from the C++ implementation.
- **Port randomization in hole punching can trigger firewall alerts.** Mitigation: document expected UDP behavior for operator firewall configurations.
- **NAT type can change dynamically** (e.g., when an upstream router restarts). Mitigation: periodic re-classification at a configurable interval.

## Migration / Rollout

1. Add NAT type field to EdgeTelemetry protobuf. Helmsman reports it; Foghorn logs it. No scoring change yet.
2. Enable NAT-aware scoring behind a feature flag. Monitor routing decisions.
3. Deploy built-in coordination server alongside existing Foghorn. C++ MistUtilFoghorn remains available as fallback.
4. Add ICE server injection to balancer responses. SDK updates ship independently.

## Open Questions

- Should the Go Foghorn coordination server be wire-compatible with the C++ UDP protocol? Backwards compatibility enables gradual migration but constrains the protocol design.
- What re-classification interval for NAT type? Too frequent wastes STUN traffic; too infrequent misses changes.
- How does this interact with Privateer (WireGuard mesh)? Mesh nodes bypass NAT entirely — should they be excluded from NAT scoring?

## References, Sources & Evidence

- [External evidence] Upstream MistServer `lib/foghorn.cpp` (NAT classification + hole punching)
- [External evidence] Upstream MistServer `lib/foghorn.h`
- [External evidence] Upstream MistServer `lib/stun.cpp` (STUN protocol implementation)
- [External evidence] Upstream MistServer `src/utils/util_foghorn.cpp` (coordination server)
- [External evidence] Upstream MistServer `nautophone/` (monitoring UI)
- [Evidence] `ansible/collections/ansible_collections/frameworks/infra/roles/mistserver/` (MistServer role only; no MistUtilFoghorn role found)
- [Evidence] `api_balancing/internal/balancer/balancer.go` (current scoring model)
- [Evidence] `npm_player/packages/core/src/core/PlayerInterface.ts` (iceServers via as any)
- [Evidence] `npm_studio/packages/core/src/types.ts` (optional iceServers config)
- [Reference] `docs/rfcs/workload-cost-model.md` (consumer of the inter-server network-performance signal)
- [Reference] RFC 8489 (STUN)
- [Reference] RFC 8656 (TURN)

# RFC: NAT Traversal as a Platform Capability

## Status

Draft

## TL;DR

- Go Foghorn should inherit MistServer's C++ Foghorn NAT traversal capabilities.
- NAT type classification becomes a scoring signal in viewer routing.
- Hole punching coordination becomes a platform-managed feature.
- Nautophone monitoring gets absorbed into operator dashboards.

## Current State

MistServer's C++ Foghorn (`mistserver/lib/foghorn.cpp`, `mistserver/src/utils/util_foghorn.cpp`) implements a complete NAT hole punching coordination system: SHA256-authenticated UDP packets, NAT type classification (OPEN, CONSISTENT, PREDICTABLE, IMPENETRABLE), multi-attempt punching with port randomization, and background punch threads. MistServer's STUN library (`mistserver/lib/stun.cpp`) implements the full STUN protocol. `MistUtilFoghorn` runs as a standalone coordination server on port 7077.

Nautophone (`mistserver/nautophone/`) provides a web UI and Node.js translator service for monitoring endpoint states.

Go Foghorn (`api_balancing/`) replaced C++ MistUtilLoad (the load balancer) but has NOT absorbed C++ Foghorn's NAT traversal capabilities. The two Foghorns serve different purposes today.

SDKs partially support ICE server configuration: `npm_studio` WhipClient accepts optional `iceServers` in its config (`types.ts:88-90`). `npm_player` NativePlayer casts `iceServers` from the source via `as any` — it is NOT in the `StreamSource` interface.

There is no platform-managed TURN/STUN infrastructure. Operators must deploy MistUtilFoghorn manually alongside edge nodes.

Evidence:

- `mistserver/lib/foghorn.cpp`
- `mistserver/lib/foghorn.h`
- `mistserver/lib/stun.cpp`
- `mistserver/src/utils/util_foghorn.cpp`
- `mistserver/nautophone/`
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

### Phase 1: NAT type as scoring signal

Foghorn already scores edges by CPU, RAM, bandwidth, and geo distance. Add NAT type as a new scoring dimension. OPEN edges receive a bonus for WebRTC viewers; IMPENETRABLE edges get deprioritized. Edge nodes report their NAT type via Helmsman (MistServer already classifies this internally). New field in the EdgeTelemetry protobuf message.

### Phase 2: Built-in coordination server

Port C++ MistUtilFoghorn coordination logic into Go Foghorn. Add the Foghorn UDP protocol on port 7077 alongside existing gRPC (18019) and HTTP (18008). The C++ implementation is approximately 660 lines — well-scoped for a port.

### Phase 3: Platform-managed hole punching

When Foghorn routes a viewer to an edge, if both sides have compatible NAT types (e.g., CONSISTENT to CONSISTENT), Foghorn coordinates the punch automatically. If both sides are IMPENETRABLE, Foghorn routes through an OPEN edge as a relay instead.

### Phase 4: ICE server injection

Foghorn injects `iceServers` into balancer responses with short-lived TURN credentials when relay is needed. Add `iceServers` to the `StreamSource` interface in `npm_player`. SDKs auto-consume platform-provided ICE servers without operator configuration.

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

- [Evidence] `mistserver/lib/foghorn.cpp` (NAT classification + hole punching)
- [Evidence] `mistserver/lib/foghorn.h`
- [Evidence] `mistserver/lib/stun.cpp` (STUN protocol implementation)
- [Evidence] `mistserver/src/utils/util_foghorn.cpp` (coordination server)
- [Evidence] `mistserver/nautophone/` (monitoring UI)
- [Evidence] `api_balancing/internal/balancer/balancer.go` (current scoring model)
- [Evidence] `npm_player/packages/core/src/core/PlayerInterface.ts` (iceServers via as any)
- [Evidence] `npm_studio/packages/core/src/types.ts` (optional iceServers config)
- [Reference] RFC 8489 (STUN)
- [Reference] RFC 8656 (TURN)

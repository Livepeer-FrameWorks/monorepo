# RFC: WireGuard Mesh with OSPF

## Status

Draft

## TL;DR

- Current WireGuard mesh uses explicit peer lists (static mesh).
- OSPF over WireGuard could reduce config complexity as node count grows.
- Evaluate hub-and-spoke or partial mesh with dynamic routing.

## Current State

- Privateer syncs mesh peers from Quartermaster and applies WireGuard configs per node.
- No dynamic routing layer is present; routing is implicit in peer configs.

Evidence:

- `api_mesh/internal/agent`
- `pkg/proto`

## Problem / Motivation

Static mesh configuration scales poorly as node count grows and increases operational overhead for topology changes.

## Goals

- Reduce O(n^2) peer management overhead.
- Allow automatic reroute on link failure.
- Keep WireGuard as the transport.

## Non-Goals

- Replacing WireGuard.
- Full BGP or internet routing.

## Proposal

- Run OSPF (via BIRD) inside WireGuard tunnels.
- Use hub-and-spoke or partial mesh to limit peer count.

## Impact / Dependencies

- Privateer mesh config generation.
- Node provisioning (BIRD + OSPF configs).
- Operational runbooks and monitoring.

## Alternatives Considered

- Keep static full mesh.
- Use a third-party mesh solution (Tailscale/Nebula).

## Risks & Mitigations

- Risk: routing instability. Mitigation: staged rollout + strong monitoring.
- Risk: harder debugging. Mitigation: clear runbooks and tooling.

## Migration / Rollout

1. Prototype OSPF on a small test cluster.
2. Deploy to one region with fallback to static mesh.
3. Expand to additional regions.

## Open Questions

- What is the current maximum node count target?
- Do we need BFD for sub-second failover?

## References, Sources & Evidence

- `api_mesh/internal/agent`
- `pkg/proto`

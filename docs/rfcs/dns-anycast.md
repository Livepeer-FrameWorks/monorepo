# RFC: Self-Hosted Global Anycast DNS

## Status

Draft

## TL;DR

- Evaluate running our own Anycast DNS to reduce vendor lock-in and enable app-aware routing.
- This is a high-risk infra decision; keep it research-only until clear need exists.

## Current State

- Navigator (`api_dns`) exists in the repo and is intended to manage platform‑managed tenant subdomains, but it is not wired into dev runtime (no service in default dev compose).
- There is no Anycast/BGP stack in dev or CI.
- No PowerDNS/Galera/IXP tooling is present in this repo.

Evidence:

- `api_dns/`
- dev compose service definitions

## Problem / Motivation

Managed DNS limits routing flexibility and introduces vendor lock-in. Anycast could enable lower latency and app-aware routing at the DNS layer. Separately, platform‑managed tenant subdomain provisioning is not implemented today for self-hosted edge clusters.

## Goals

- Define decision criteria for building vs buying DNS.
- Identify minimum viable architecture for Anycast DNS.
- Clarify the baseline for platform‑managed tenant subdomain provisioning (not BYO).

## Non-Goals

- Writing a full BGP/IXP runbook in the RFC.
- Committing to a specific provider or ASN now.
- BYO custom domains (explicitly out of scope for now).

## Proposal

- Treat this as a decision RFC with explicit criteria (latency, cost, control, risk).
- If criteria are met, proceed with a dedicated ops plan and runbooks outside the RFC.

## Impact / Dependencies

- Network operations (BGP, IRR/RPKI, IXPs).
- DNS stack (PowerDNS or equivalent).
- Database HA for DNS records.

## Alternatives Considered

- Managed DNS providers with geo/routing features.
- Hybrid model: managed DNS + regional routing services.

## Risks & Mitigations

- Risk: misconfigured BGP can cause outages. Mitigation: staged rollout + strict runbooks.
- Risk: cost and operational overhead. Mitigation: start with a small pilot.

## Migration / Rollout

1. Define decision criteria and success metrics.
2. Run a limited pilot in one region.
3. Expand to multi-region Anycast if stable.

## Open Questions

- What latency/cost thresholds justify Anycast ownership?
- Who owns 24/7 ops for BGP/DNS?

## Glue Records

DNS autonomy requires glue records — A/AAAA records held at the TLD registry that point to your nameservers' IPs. Without glue records, DNS resolution for your domain depends on another provider's nameservers resolving first. Configure glue records at the registrar level for each authoritative nameserver.

## Multi-Registrar, Multi-TLD Strategy

No single registrar should be a SPOF. Distribute domains across at least 2 registrars and use multiple TLDs. If a registrar makes an opaque policy decision (as documented in the General Research "How to Seed a Cloud" case with Amazon Business Prime), operations continue on alternate domains.

## RIR Strategy

Acquire IP blocks from multiple Regional Internet Registries:

- ARIN (North America): IPv4 waiting list + IPv6 allocation
- RIPE NCC (Europe): Direct allocation for European presence
- APNIC (Asia-Pacific): For SE Asian edge locations

Ethical acquisition: avoid extractivist practices in IP space markets. Purchase clean IPv4 blocks with documented transfer history. Reference: General Research AS13362 as a real-world implementation of multi-RIR strategy.

## RPKI & IRR

Route Origin Validation prevents BGP hijacking. Publish ROA records for all announced prefixes. Register routes in Internet Routing Registry (IRR) databases. Require RPKI validation from transit providers.

## BGP Failover as Complement to Foghorn Federation

Foghorn federation handles cross-cluster routing at the application layer (gRPC). BGP handles failover at the network layer — if a facility goes offline (power failure, physical attack, network partition), BGP reroutes traffic automatically before any application-layer health check fires.

These are complementary, not competing:

- BGP: Fast failover (seconds), coarse-grained (entire prefix), no application awareness
- Foghorn federation: Slower failover (depends on PeerHeartbeat 10s interval), fine-grained (per-stream, per-viewer), full application awareness

Both are needed for production resilience.

## Cloudflare Dependency Risk

Navigator currently depends on Cloudflare API for DNS record management and certificate issuance. Cloudflare could restrict API access, change pricing, or enforce policy decisions with no recourse — the same class of risk documented in General Research's Amazon Business Prime incident. The PowerDNS self-hosted path (Phase 4) is the mitigation. Until then, Cloudflare is the remaining managed dependency.

## References, Sources & Evidence

- [Evidence] `api_dns/`
- [Evidence] dev compose service definitions
- [Source] External DNS/BGP/Anycast research (TBD)
- [Source] General Research AS13362 — real-world multi-RIR, self-hosted DNS implementation
- [Source] "How to Seed a Cloud" (generalresearch.com/detail-oriented/how-to-seed-a-cloud/)

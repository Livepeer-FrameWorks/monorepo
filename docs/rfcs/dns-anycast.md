# RFC: Self-Hosted Global Anycast DNS

## Status
Draft

## TL;DR
- Evaluate running our own Anycast DNS to reduce vendor lock-in and enable app-aware routing.
- This is a high-risk infra decision; keep it research-only until clear need exists.

## Current State (as of 2026-01-13)
- Navigator (`api_dns`) exists in the repo and is intended to manage platform‑managed tenant subdomains, but it is not wired into dev runtime (no service in `docker-compose.yml`).
- There is no Anycast/BGP stack in dev or CI.
- No PowerDNS/Galera/IXP tooling is present in this repo.

Evidence:
- `api_dns/`
- `docker-compose.yml`

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

## References, Sources & Evidence
- [Evidence] `api_dns/`
- [Evidence] `docker-compose.yml`
- [Source] External DNS/BGP/Anycast research (TBD)

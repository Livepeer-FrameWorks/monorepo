# RFC: Self-Hosted Global Anycast DNS

## Status

Draft. Evidence-gated research with no target release or delivery date.

## TL;DR

- Keep Cloudflare and Bunny as the production authoritative-DNS path. Navigator already
  combines managed DNS with application-aware media placement; an ASN is not required for
  the current geo-routing product.
- Consider self-hosted authoritative Anycast DNS only when managed providers create a
  measured control, reliability, or cost constraint that Foghorn cannot solve at the
  application layer.
- DNS anycast and media-egress peering are separate decisions. Network-cost measurement and
  peering are scoped in `docs/rfcs/network-egress-peering.md`.

## Owning Services / Modules

Navigator (`api_dns`) owns managed public DNS and certificate automation. No service owns a
self-hosted authoritative DNS or BGP runtime today, and no such runtime exists in this repository.

## Current State

Navigator reconciles public records from Quartermaster inventory:

- Cloudflare remains authoritative for root, API, web, admin, and support names.
- Bunny hosts delegated media-cluster, global media-entrypoint, and tenant-alias zones and
  applies geolocation Smart Routing when node coordinates are available.
- Foghorn performs the application-aware decision after DNS, using current authority,
  placement policy, health, capacity, source feasibility, and geography.
- Quartermaster is the health authority for desired DNS membership. Navigator removes
  unhealthy nodes from managed record sets and preserves records when inventory is
  transiently unavailable.
- Navigator manages ACME DNS-01 issuance and distributes the resulting TLS material through
  the existing control path.

There is no authoritative DNS daemon, BGP speaker, route policy, RPKI automation, Anycast
prefix, or network operations runbook in the repository. This RFC does not imply that one
has been selected.

Evidence:

- `api_dns/README.md`
- `website_docs/src/content/docs/operators/dns.mdx`
- `docs/architecture/media-placement-policy.md`
- `api_dns/internal/provider/cloudflare/`
- `api_dns/internal/provider/bunny/`

## Problem / Motivation

Managed authoritative DNS creates provider dependencies and bounds which DNS-level routing
policies FrameWorks can express. Self-hosted anycast could eventually improve control over
authoritative behavior, routing policy, telemetry, and provider portability.

Those potential benefits do not establish a current need. DNS is only the coarse entry-routing
layer; Foghorn already makes the stream-, tenant-, health-, and capacity-aware decision. Owning
an ASN or authoritative DNS fleet solely to recreate managed geolocation records would add a
24/7 network and DNS obligation without improving the core placement decision.

## Goals

- Define the evidence required before replacing or supplementing managed authoritative DNS.
- Preserve a migration path that does not couple DNS autonomy to media-egress peering.
- State the minimum routing, security, failover, and operational properties of a future pilot.
- Keep the current managed-provider path explicit until a replacement is proven.

## Non-Goals

- Committing to an ASN, IP purchase, RIR membership, IXP, transit provider, DNS daemon, or
  database implementation.
- Assigning self-hosted DNS to a release or calendar date.
- Using DNS as a substitute for Foghorn placement or media-session admission.
- Justifying media peering through authoritative DNS traffic.
- Announcing live media prefixes through anycast.

## Decision Criteria

Self-hosted authoritative DNS should advance beyond research only when all applicable criteria
have evidence:

1. A required routing or authority behavior cannot be expressed safely through the managed
   providers plus Foghorn.
2. Measured provider incidents, concentration risk, or authoritative-query cost exceeds an
   agreed operational threshold.
3. At least two independent sites and network paths can serve the zone without sharing the
   failure being mitigated.
4. FrameWorks has named 24/7 ownership for BGP, DNSSEC, route security, abuse response,
   monitoring, incident handling, and registrar/TLD coordination.
5. A managed secondary or equivalent rollback path has been tested before production delegation.

The thresholds are an operator decision based on current traffic, contracts, and reliability
targets. This RFC deliberately does not freeze vendor prices or traffic volumes into architecture.

## Proposed Evaluation Path

### 1. Continue the managed production path

Keep Navigator's Cloudflare and Bunny integrations authoritative. Improve provider portability
at Navigator's provider boundary and retain Foghorn as the application-aware routing layer.

### 2. Prove authority portability before anycast

Export the complete desired zone state from FrameWorks-owned authority, verify deterministic
reconciliation against a second provider, and exercise DNSSEC key and delegation rollover. This
tests whether FrameWorks can change providers without first operating BGP.

### 3. Run a non-critical authoritative pilot

If the decision criteria are met, use a delegated test zone with at least two sites, independent
transit, health-triggered route withdrawal, RPKI Route Origin Authorizations, IRR objects, route
filters, query telemetry, and a managed fallback. The pilot must not begin with production media
or tenant zones.

### 4. Migrate only after failure testing

Exercise site loss, application failure with the host still reachable, route leak protection,
DNSSEC rollover, datastore loss, stale-zone recovery, and withdrawal/reannouncement. Production
delegation remains an explicit later decision rather than an automatic result of a successful lab.

## ASN and Address-Space Strategy

A public ASN is needed only when FrameWorks has a distinct external routing policy and a concrete
multihomed deployment. In the RIPE service region, an ASN request requires the routing policy and
at least two peering partners. Provider-independent IPv6 can be requested through a sponsoring LIR;
the minimum IPv6 PI assignment is a `/48`.

One globally routable prefix can be announced from multiple anycast sites. Acquiring resources
from multiple RIRs is not a default availability strategy and creates additional policy, routing,
RPKI, and operational work. IPv4 acquisition must be justified by an actual reachability need;
it is not a prerequisite for researching the DNS architecture.

## Glue Records

Glue is required when a delegated nameserver name is in-bailiwick and resolving that name would
otherwise depend on the delegation being resolved. Out-of-bailiwick nameserver names do not have
the same circular dependency. Registrar/TLD access remains part of the authority chain either way;
glue does not make DNS independent of the registry or registrar.

## BGP and Application Failover

BGP and Foghorn protect different failure domains:

- BGP directs a prefix toward a reachable site and reacts to route/session state.
- Foghorn decides whether a specific tenant, stream, protocol, and node may serve a request.

BGP is not inherently faster than Foghorn's health path. Convergence depends on topology, timers,
policy, and withdrawal propagation, and a healthy BGP session does not detect a failed DNS process.
A future deployment must couple service health to route withdrawal without allowing a transient
application fault to flap global routes. Application admission remains authoritative after traffic
reaches a site.

## Relationship to Media Peering

Authoritative DNS has low bandwidth and cannot amortize a media network. Media peering is justified
by sustained viewer egress, destination-network concentration, quality, and the fully loaded cost
of transit, interconnection, equipment, facilities, mitigation, and operations. It is evaluated in
`docs/rfcs/network-egress-peering.md`; either proposal may remain unbuilt while the other advances.

## Risks and Mitigations

- **Route leak or hijack:** publish ROAs, maintain IRR objects, filter prefixes and AS paths, and
  test maximum-prefix limits with every upstream.
- **Reachable site with failed DNS:** health-gated withdrawal plus local process supervision and
  independent external probes.
- **Control-plane or datastore failure:** serve signed, last-known-good zone state locally and test
  bounded recovery; never require a central database read per query.
- **DNSSEC failure:** documented key ceremony, parent DS rollover, offline recovery material, and
  rehearsed rollback.
- **Operational overload:** do not migrate without named on-call ownership and a managed fallback.
- **Provider concentration merely moves inward:** use independent sites and upstreams and preserve
  the ability to delegate back to a managed provider.

## Open Questions

- Which concrete managed-provider limitation, incident rate, or cost would trigger a pilot?
- Is the desired end state self-hosted primary with managed secondary, managed primary with a
  self-hosted secondary, or full self-hosted authority?
- Which health signal is sufficiently local and trustworthy to withdraw a route?
- Who owns 24/7 BGP, DNSSEC, registrar, and abuse operations?

## References, Sources, and Evidence

- [Evidence] `api_dns/README.md`
- [Evidence] `website_docs/src/content/docs/operators/dns.mdx`
- [Evidence] `docs/architecture/media-placement-policy.md`
- [Source] [RIPE NCC: Request an AS Number](https://www.ripe.net/manage-ips-and-asns/as-numbers/request-an-as-number/)
- [Source] [RIPE NCC: Request an IPv6 PI Assignment](https://www.ripe.net/manage-ips-and-asns/ipv6/request-ipv6/how-to-request-an-ipv6-pi-assignment/)
- [Source] [RFC 4786: Operation of Anycast Services](https://www.rfc-editor.org/rfc/rfc4786)
- [Source] [RFC 7454: BGP Operations and Security](https://www.rfc-editor.org/rfc/rfc7454)
- [Source] [RFC 1034: Domain Names - Concepts and Facilities](https://www.rfc-editor.org/rfc/rfc1034)

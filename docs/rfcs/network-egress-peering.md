# RFC: Media Egress Economics and Peering

## Status

Draft. Evidence-gated option with no target release, implementation date, or procurement commitment.

## TL;DR

- Keep managed DNS and managed delivery/overflow as the operational baseline while FrameWorks
  measures where viewer traffic actually goes and what each serving path really costs.
- Build an internal supply-cost model and destination-ASN traffic matrix before acquiring network
  resources or selecting an exchange. Tenant-facing placement prices are not infrastructure cost.
- Consider an owned transit/peering point of presence only when one metro has sustained demand,
  two independent upstream paths, a material fully loaded saving, and named 24/7 operations.
- An ASN enables external routing policy; it does not itself reduce bandwidth cost. Self-hosted
  authoritative DNS remains a separate decision in `docs/rfcs/dns-anycast.md`.

## Owning Services / Modules

This proposal crosses existing ownership boundaries without assigning implementation:

- Periscope owns measured traffic and quality facts.
- Purser owns tenant tariffs and invoices, but must not become the owner of confidential carrier
  cost or routing policy merely because it already computes customer charges.
- Quartermaster owns cluster, node, operator, region, and capacity-owner identity.
- Foghorn consumes approved placement facts and may eventually compare internal supply cost in a
  shadow or operator policy path.
- A future network-operations owner would own ASN, address space, BGP, transit, peering, route
  security, DDoS response, and carrier contracts. That owner does not exist in the repository today.

## Current State

FrameWorks already has most of the application-level foundation needed to measure a network case:

- Navigator uses Cloudflare for root/control DNS and Bunny for delegated media and tenant zones.
- Foghorn performs live placement using signed tenant/object authority, capacity-owner consent,
  geography, health, directional capacity, source feasibility, and exact-destination preparation.
- Periscope attributes finalized viewer egress and quality to serving and origin clusters.
- Regional metering emits cluster-attributed `egress_gb` usage to Purser.
- The placement policy can request fresh comparable commercial quotes for a tenant and consumption
  bundle.
- Managed hybrid edges let customer or provider capacity join the media plane without giving the
  edge operator control of Foghorn or the platform authority plane.

Important gaps remain:

- No analytics fact identifies the viewer's destination ASN. Geography cannot show whether traffic
  is concentrated in a peerable eyeball network.
- No internal record models transit commits, 95th-percentile billing, IXP/PNI ports, cross-connects,
  colocation, power, router/optics amortization, DDoS mitigation, spares, remote hands, or on-call.
- Purser's placement quote is the tenant's incremental unwaived usage charge. It explicitly excludes
  already-paid fixed access fees and unrelated costs. It is not FrameWorks' marginal or fully loaded
  supply cost.
- Application telemetry identifies the serving node and cluster, but not which BGP route, transit,
  or peer carried the packets. Route-level attribution would require network telemetry at an owned
  routing boundary.
- There is no ASN, provider-independent prefix, BGP configuration, RPKI/IRR automation, IXP tooling,
  or network runbook in the repository.

Evidence:

- `website_docs/src/content/docs/operators/dns.mdx`
- `docs/architecture/media-placement-policy.md`
- `docs/architecture/meter-contracts.md`
- `docs/architecture/routing-events-attribution.md`
- `docs/architecture/viewer-routing.md`
- `README.md`

## Problem / Motivation

Cloud and CDN egress is usually sold per byte. Wholesale transit and peering are commonly sold as
ports, commits, or 95th-percentile capacity, so high sustained utilization can make their effective
cost per delivered GiB much lower. Live video can therefore justify owned interconnection once
enough demand is concentrated behind the same metro and destination networks.

That saving is not automatic. Live traffic is sustained, correlated around events, and sensitive
to loss, congestion, and failover. A port in Amsterdam cannot economically serve a viewer mix that
is mostly elsewhere, and an IXP reaches only the routes its peers advertise. Transit, facilities,
redundancy, DDoS protection, hardware, and operations remain necessary. Comparing only a public
IXP port fee with a CDN per-GB rate produces a false business case.

Netflix Open Connect is a useful operating principle but not a direct cache model for FrameWorks.
Netflix can pre-position repeatedly watched VOD objects and evaluates substantial existing ISP
traffic before embedding appliances. Live media cannot be prefetched, and long-tail streams may
have little same-location fan-out. FrameWorks should copy the evidence-first interconnection and
localization discipline, not assume Netflix's cache efficiency.

## Goals

- Measure delivered traffic by serving cluster, metro, destination ASN, protocol, content kind,
  and time window without retaining viewer IP addresses.
- Model both marginal and fully loaded supply cost for each provider/facility path.
- Forecast 95th-percentile demand, burst headroom, and failure scenarios from observed traffic.
- Compare managed delivery, dedicated/unmetered hosts, transit, public peering, private peering,
  and embedded partner capacity on equivalent reliability and quality assumptions.
- Run cost-aware choices in shadow mode before they can influence production placement.
- Define objective approval gates for ASN acquisition, provider engagement, and a one-metro pilot.
- Preserve managed delivery as overflow and rollback unless a later decision explicitly changes it.

## Non-Goals

- Committing to a release, date, metro, exchange, transit provider, CDN, ASN, or IP purchase.
- Replacing Bunny/Cloudflare DNS or implementing authoritative anycast DNS.
- Announcing live media service prefixes through anycast.
- Treating customer prices, operator credits, or marketplace revenue share as network supply cost.
- Building an ISP appliance program before destination-ASN traffic demonstrates mutual value.
- Assuming peering is settlement-free or available merely because two networks share an exchange.
- Designing customer-facing generic CDN services; that remains `generic-proxying-cdn` in the
  platform feature registry.

## Cost Model

### Supply contract

The internal model needs an effective period and source evidence for each serving path:

```text
NetworkSupplyContract
  provider and contract identity
  cluster, facility, metro, and currency
  path kind: managed_delivery | dedicated_host | transit | ixp | pni | embedded_partner
  port capacity and committed/burst bandwidth
  billing model and percentile/sample rules
  recurring port, transit, cross-connect, colo, power, mitigation, and support cost
  equipment, optics, spares, and installation amortization
  minimum term, renewal boundary, and effective interval
  included capacity, overage terms, credits, and known exclusions
  evidence source and last verification time
```

Carrier contracts and negotiated rates are operator-confidential. Tenant, marketplace, GraphQL,
MCP, and public analytics surfaces must not expose them. A derived cost class or approved bounded
score may cross into placement; raw contract terms do not.

### Demand facts

The demand side needs:

- bytes and delivered minutes by serving cluster and metro;
- average, peak, and contract-equivalent 95th-percentile bitrate;
- destination ASN and country/region distribution;
- concurrent viewers, stream fan-out, and content kind;
- origin-pull and inter-cluster transfer required by the serving choice;
- quality outcomes including first-frame time, rebuffering, failures, and protocol;
- expected demand after the largest relevant node/path failure.

Destination ASN should be resolved from the trusted client address at the existing GeoIP boundary
and persisted as a bounded network attribute. Raw viewer IP does not need to enter analytics. ASN
data is routing evidence, not identity or permission, and missing lookup data must remain unknown.

### Marginal and fully loaded views

Both views are required:

- **Marginal cost** answers whether one additional unit fits inside an existing commit or creates
  overage/percentile exposure.
- **Fully loaded cost** allocates recurring facilities, equipment, mitigation, support, and
  operational cost across realistically deliverable traffic with required headroom.

A path can be cheap at the margin and still be a bad investment when its fixed cost, concentration
risk, or under-utilization is included. Conversely, sunk committed capacity can be the correct
short-term route even when its fully loaded historical cost is high. Reports must label the view
rather than collapse them into one number.

## Evaluation Path

### 1. Establish the managed baseline

Record actual managed delivery invoices, discounts, regions, effective rates, quality, and incident
history. Public list prices are useful for orientation but are not the approval baseline when a
negotiated contract exists.

### 2. Add destination-network measurement

Extend the analytics contract with privacy-preserving destination ASN attribution and produce a
traffic matrix by serving metro and destination ASN. Retain enough history to capture ordinary
traffic, event bursts, seasonality, and provider incidents before forecasting a port commitment.

### 3. Build an operator-only cost ledger

Store effective-dated provider contracts and compute marginal and fully loaded costs from measured
traffic. Keep this ledger outside tenant tariff resolution. Reconcile forecasts against invoices
before any result is trusted for routing.

### 4. Run shadow comparisons

For each real placement decision, compute the hypothetical managed, transit, and peering outcome
without changing the selected destination. Compare cost, headroom, quality, and failure behavior.
Unknown contract, traffic, route, or quality evidence makes the comparison unavailable rather than
cheap.

### 5. Obtain quotes and network resources only after evidence

Provider and exchange quotes can begin when one metro's observed demand is large enough to model a
real port and redundant upstreams. A public ASN and provider-independent address space should be
requested only after FrameWorks has the distinct external routing policy and partner information
required by the applicable RIR.

### 6. Pilot one metro behind explicit approval

A pilot remains conditional on the gates below. It uses concrete unicast edge addresses and
Foghorn placement, at least two independent upstream paths, RPKI/IRR and route filters, tested DDoS
handling, capacity reserves, and managed overflow. Public peering may start through remote peering
or a smaller port when that produces a better reversible test than immediate colocation.

### 7. Expand or stop based on measured results

Compare invoices, route telemetry, quality, incidents, and staff load with the shadow model. A pilot
that does not meet the approved saving and reliability margin is removed or held at its useful
capacity; it does not create a presumption that more metros must follow.

## Decision Gates

No single public traffic threshold is universally correct. A proposal to activate owned peering
must provide all of the following with current evidence:

1. A sustained traffic history for the candidate metro and destination networks.
2. Written quotes for managed delivery, two independent upstream paths, facility/cross-connect,
   equipment, mitigation, and support.
3. A failure model showing capacity and cost after loss of the largest required path or node.
4. A fully loaded saving that exceeds an explicitly approved safety margin, not a nominal saving
   based only on transit or IXP port price.
5. Acceptable payback under conservative utilization and contract-term assumptions.
6. A route/peer analysis showing that candidate peers carry a material share of the traffic and
   will actually establish the required sessions.
7. Named 24/7 operational ownership, escalation contacts, monitoring, spares, remote hands, and a
   tested managed-delivery overflow path.
8. A security review covering RPKI, IRR, prefix and AS-path filters, maximum-prefix limits, BGP
   session authentication where supported, and DDoS response.

Approval of measurement does not approve procurement. Approval of an ASN does not approve a PoP.
Approval of one PoP does not approve expansion or self-hosted authoritative DNS.

## Placement Integration

Internal supply cost must be a distinct, effective-dated input. It must not overwrite Purser's
tenant-facing `PlacementCommercialFacts` or change what an invoice means. A future routing policy
may combine:

- hard authority, consent, residency, protocol, source, and capacity eligibility;
- tenant-visible commercial preference within the tenant's allowed clusters;
- operator supply-cost preference within otherwise equivalent platform-operated capacity;
- geography and measured quality bounds;
- deterministic headroom and stable tie-breaking.

Operator cost cannot grant access, bypass a tenant's placement policy, select an unsubscribed
marketplace cluster, or turn missing evidence into a cheap path. Cost-aware routing should remain
shadow-only until invoice reconciliation and failure tests establish accuracy.

## Relationship to Anycast and Embedded Edges

- **Authoritative DNS anycast:** low-bandwidth control infrastructure evaluated independently in
  `docs/rfcs/dns-anycast.md`.
- **Media anycast:** not proposed. Foghorn-directed unicast destinations preserve stream-aware
  placement, exact admission, debugging, and controlled draining.
- **ISP-embedded edges:** a possible later interconnection form only where traffic into a specific
  eyeball ASN is large enough to benefit both parties. The Netflix model supports this evidence-first
  principle; live media still has different cache and fill behavior.
- **Customer-operated hybrid edges:** already reduce provider egress when the tenant supplies
  useful capacity and placement permits it. Their tenant-visible price and operator-incurred
  network cost remain separate facts.

## Risks and Mitigations

- **List-price fantasy:** require written quotes and invoice reconciliation.
- **Under-utilized fixed capacity:** model conservative demand, minimum terms, and failure headroom.
- **Peering coverage mistaken for Internet coverage:** keep redundant transit and measure route mix.
- **Cost optimization harms viewers:** enforce quality bounds and shadow-test before activation.
- **Event bursts cross the commit:** model the provider's real percentile/sample method and retain
  managed overflow.
- **Traffic attribution leaks viewer data:** persist destination ASN and coarse geography, not raw IP.
- **Confidential carrier terms leak to tenants:** isolate raw contracts and expose only approved
  derived operator facts.
- **Operational burden erases savings:** include on-call, mitigation, hardware, spares, and remote
  hands in fully loaded cost.
- **One successful metro drives premature expansion:** require independent approval and evidence for
  each location.

## Open Questions

- Which service should own confidential network supply contracts and invoice reconciliation?
- Which ASN database and update cadence meet routing accuracy and privacy requirements?
- Can existing viewer-session facts carry destination ASN without expanding retention risk?
- Which route telemetry is required to attribute bytes to transit, IXP, PNI, or embedded paths?
- What saving margin and payback policy should approvers require for a pilot?
- Which quality and availability bounds must a cheaper route satisfy?
- Should managed overflow be selected per session, per stream, or by bounded capacity reservation?

## Market Snapshot

This snapshot supports evaluation only and must be refreshed before procurement:

- TeleGeography reported a low-end 100GE IP-transit offer of approximately
  `$0.05/Mbps/month` in competitive markets in Q2 2025. That implies `$5,000/month` for
  100,000 committed Mbps before local access, installation, equipment, redundancy, and operations.
- AMS-IX lists a 100GE Amsterdam internet-peering port at `EUR 3,240/month` on a 12-month
  term, excluding colocation, cross-connects, VAT, and optional SLA charges.
- Bunny lists its volume CDN at `$0.005/GB` for the first 500 TB, `$0.004/GB` from
  500 TB to 1 PB, and `$0.002/GB` from 1 PB to 2 PB. These public tiers are a managed
  benchmark, not proof that they match FrameWorks' contract, regions, traffic, or quality needs.
- Netflix states that embedded Open Connect appliances are considered based on substantial existing
  ISP traffic and require a public ASN, interconnection, capacity, facilities, and control-plane
  connectivity.

## References, Sources, and Evidence

- [Evidence] `docs/architecture/media-placement-policy.md`
- [Evidence] `docs/architecture/meter-contracts.md`
- [Evidence] `docs/architecture/routing-events-attribution.md`
- [Evidence] `website_docs/src/content/docs/operators/dns.mdx`
- [Source] [TeleGeography: IP Transit Pricing in 2025](https://resources.telegeography.com/ip-transit-price-erosion-significant-regional-differences-remain)
- [Source] [AMS-IX Amsterdam Pricing](https://www.ams-ix.net/ams/pricing)
- [Source] [Bunny Pricing](https://bunny.net/pricing/)
- [Source] [RIPE NCC: Request an AS Number](https://www.ripe.net/manage-ips-and-asns/as-numbers/request-an-as-number/)
- [Source] [Netflix Open Connect](https://openconnect.netflix.com/)
- [Source] [Netflix Open Connect Deployment Guide](https://openconnect.netflix.com/deploymentguide.pdf)

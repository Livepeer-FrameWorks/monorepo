# RFC: Capacity Reserves, Fleet Reporting, and Alerts

## Status

Partially implemented. Live placement already enforces explicit capacity state and directional
bandwidth headroom. Reserve policy below physical exhaustion, fleet failure-domain reporting, and
the capacity/topology alert set remain proposals with no target release or delivery date.

## TL;DR

- Keep live capacity admission in the shared `pkg/placement` path. The legacy weighted balancer
  no longer selects live viewers or publishers.
- Add operator-defined reserve floors before a node reaches 100% CPU, RAM, or bandwidth, and
  revalidate those floors during exact-destination preparation and final admission.
- Report usable cluster capacity and failure-domain loss scenarios; treat N\*2 as an operator
  policy, not a universal hard-coded routing rule.
- Extend the existing Prometheus rules with capacity and topology alerts once their source metrics
  and notification ownership are defined.

## Current State

Live ingest and viewer routing use `pkg/placement`. Candidate observations carry an explicit
capacity state, directional `BWAvailable`/`BWLimit`, CPU percentage, RAM usage, freshness, and
expiry. The evaluator rejects stale, unknown, unavailable, invalid, and exhausted capacity. A
candidate with zero directional bandwidth is exhausted, and unknown preferred capacity cannot be
treated as proof of exhaustion that permits paid spillover.

Within an eligible placement group, the evaluator orders equal-distance candidates by relative
bandwidth headroom, CPU, and RAM headroom. Exact-destination preparation rechecks directional
capacity before returning a destination, and final media admission remains a separate fence.

The observation adapter currently marks a node exhausted when directional bandwidth reaches zero,
CPU reaches 100%, or RAM reaches its maximum. There is no declarative reserve floor that can keep,
for example, 20% egress headroom or CPU capacity for an ingest/transcode failure. Ranking therefore
reduces pressure before exhaustion but does not express an operator's redundancy budget.

The weighted scorer in `api_balancing/internal/balancer` remains for stored-media candidate ranking,
node-bound source lookup, and candidate answers returned to peers. Changing only that scorer would
not protect live traffic.

vmalert evaluates the rules embedded from `pkg/grafana/rules/frameworks.yml`, which cover a broad set
of service, authority, metering, settlement, and multi-region replication failures, and notifies
Alertmanager, which routes by severity and region. Capacity/topology gaps remain: there is no
dedicated edge-bandwidth saturation rule, federation peer-loss rule, replication shortfall rule,
TLS-expiry rule, or Foghorn leadership-churn rule.

No `frameworks_cluster_*_utilization_ratio` or failure-domain reserve gauges exist. Per-node facts
and cluster-load summaries do not answer whether a cluster can lose its largest node, facility, or
uplink while preserving admitted service.

Evidence:

- `pkg/placement/types.go`
- `pkg/placement/evaluate.go`
- `api_balancing/internal/balancer/placement_observation.go`
- `api_balancing/internal/federation/placement_policy_gate.go`
- `docs/architecture/media-placement-policy.md`
- `docs/architecture/viewer-routing.md`
- `pkg/grafana/rules/frameworks.yml`
- `ansible/collections/ansible_collections/frameworks/infra/roles/prometheus_stack/templates/alertmanager.yml.j2`

## Problem / Motivation

Physical exhaustion is too late for a resilient media fleet. A node at 98% egress or a cluster that
cannot survive one node loss may still be technically available, but admitting more sessions can
turn a routine failure into viewer impact. Operators need to reserve capacity for burst, failover,
measurement error, and non-viewer workloads before the hard limit is reached.

Capacity policy must preserve the placement engine's current safety semantics. Missing or stale
telemetry is uncertainty, not spare capacity and not proof that a preferred pool is full. A reserve
breach may permit fallback only when the active placement policy explicitly allows capacity
spillover.

Operational reporting has a related gap. Aggregate utilization alone can hide concentration: 40%
fleet utilization may still be unsafe if one facility carries most of the usable egress. Reporting
must show both aggregate headroom and the result of losing important failure domains.

## Goals

- Configurable reserve policy for CPU, RAM, and directional bandwidth.
- One capacity classification shared by preview, live evaluation, destination preparation, and
  final admission.
- Cluster-level usable-capacity, reserved-capacity, and utilization metrics.
- Failure-domain views for at least node and cluster/facility loss where inventory can identify
  those domains.
- Alerts for sustained capacity pressure and material topology degradation.
- Explainable placement reasons when reserve policy excludes a candidate or permits spillover.

## Non-Goals

- Autoscaling or infrastructure procurement.
- Predictive processing-resource cost; that belongs to `docs/rfcs/workload-cost-model.md`.
- Provider transit, CDN, or peering economics; that belongs to
  `docs/rfcs/network-egress-peering.md`.
- A universal 50% utilization requirement. N\*2 is one possible operator policy and may be
  inappropriate for heterogeneous or multi-failure-domain fleets.
- Restoring the legacy weighted scorer as the live routing authority.

## Proposal

### 1. Define reserve policy at the capacity-owner boundary

Represent reserves as capacity-owner policy associated with the cluster or node class. The exact
management surface requires a separate reviewed design, but the compiled runtime inputs should be
explicit and directional:

- minimum free egress bandwidth or maximum egress utilization;
- minimum free ingress bandwidth or maximum ingress utilization;
- maximum CPU utilization;
- maximum RAM utilization;
- optional emergency reserve that only specifically authorized traffic may consume.

Environment overrides may remain useful for deployment emergencies, but they must not become a
second policy language with different preview and runtime behavior.

### 2. Classify reserve breaches before placement

The observation adapter should derive candidate capacity from physical availability minus active
reservations and the applicable reserve. A reserve breach produces an explicit, explainable
capacity outcome. It must not be represented as a low weighted score.

The same policy revision and measurements must be rechecked during exact-destination preparation.
Final admission must not widen a rejected decision if measurements changed between resolution and
connection. Conversely, an unavailable dependency must remain unavailable rather than being
reported as exhausted.

When every candidate in a preferred group is verifiably exhausted by the same policy, the existing
placement spillover mode decides whether a lower group may be used. There is no fallback that sends
traffic back to a saturated node merely because all nodes crossed the threshold.

### 3. Report fleet capacity and failure tolerance

Export cluster metrics for each resource and direction:

- physical capacity;
- observed use;
- active reservations;
- policy reserve;
- usable headroom after reserve;
- stale, unknown, unavailable, and exhausted node counts.

Also compute loss scenarios from known failure domains, beginning with the largest-node loss and
expanding to facility/uplink loss only when inventory carries trustworthy ownership. Report the
post-loss usable headroom rather than only a boolean N\*2 label.

An operator may alert on 50% aggregate utilization to preserve N\*2 capacity, but FrameWorks should
not assume that threshold proves resilience across unequal nodes or correlated sites.

### 4. Add capacity and topology alerts

Add rules to `pkg/grafana/rules/frameworks.yml` only after stable source metrics exist:

- sustained directional bandwidth reserve breach;
- cluster post-failure headroom below policy;
- federation peer disconnection outside an expected maintenance window;
- requested replication durability below policy;
- internal and public TLS certificate expiry;
- excessive Foghorn leadership churn.

Alert thresholds and `for` durations belong to operator configuration. Warning and critical levels
must avoid paging on a single stale sample or expected drain.

### 5. Keep legacy consumers scoped

Stored-media and source-candidate paths that still use the weighted scorer may adopt equivalent
hard reserve classification, but only through their current ownership boundary. The canonical docs
must continue to distinguish those consumers from live placement until the legacy scorer is retired.

## Owning Services / Modules

- Quartermaster: capacity-owner intent and failure-domain inventory.
- Foghorn: observation, reservation, exact-destination revalidation, and fleet metrics.
- `pkg/placement`: deterministic capacity eligibility and explainable spillover semantics.
- vmalert/Alertmanager: alert evaluation, routing, and paging policy.
- Chartroom/Grafana: operator-visible capacity and failure-domain reporting.

## Risks and Mitigations

- **Over-conservative reserves reject healthy demand:** preview the effective policy, expose excluded
  capacity, and require an explicit operator change rather than silent fallback.
- **Different stages classify capacity differently:** compile one policy and bind its revision to
  observation, preparation, and admission evidence.
- **Stale telemetry looks like exhaustion:** preserve the current unknown/unavailable states and
  allow capacity spillover only on complete, fresh evidence.
- **Aggregate metrics hide correlated failure:** report node and known facility/uplink loss scenarios.
- **Alert fatigue:** require sustained conditions, maintenance awareness, and an identified receiver.
- **Tenant preference overrides owner safety:** capacity-owner reserve is a hard upper bound;
  consumer placement policy may narrow capacity but cannot expand it.

## Open Questions

- Which service owns the durable reserve-policy record and its reviewed management workflow?
- Should emergency reserve be consumable by existing-session recovery only, or also by selected
  tenant/service classes?
- Which inventory fields are trustworthy enough to define facility and uplink failure domains?
- How should established sessions be treated after a reserve changes: drain naturally, migrate
  where protocol permits, or only block new admissions?
- Which alerts are platform-operated versus delegated to a self-hosted capacity owner?

## References, Sources, and Evidence

- [Evidence] `pkg/placement/types.go` (capacity state and directional bandwidth facts)
- [Evidence] `pkg/placement/evaluate.go` (freshness, capacity, metrics, and spillover checks)
- [Evidence] `api_balancing/internal/balancer/placement_observation.go` (current exhaustion boundary)
- [Evidence] `docs/architecture/media-placement-policy.md` (live placement and preparation contract)
- [Evidence] `docs/architecture/viewer-routing.md` (legacy scorer scope)
- [Evidence] `pkg/grafana/rules/frameworks.yml` (current alert rules)
- [Reference] `docs/rfcs/workload-cost-model.md`
- [Reference] `docs/rfcs/network-egress-peering.md`

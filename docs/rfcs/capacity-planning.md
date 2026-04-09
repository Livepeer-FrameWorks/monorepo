# RFC: Capacity Planning and Utilization Thresholds

## Status

Draft

## TL;DR

- Add configurable utilization thresholds to the edge balancer so saturated nodes can be excluded from routing.
- Ship baseline Prometheus alert rules for edge saturation, peer health, and operational signals.
- Surface cluster-wide capacity reporting to support the N\*2 rule (never exceed 50% utilization).

## Current State

Foghorn's balancer (`api_balancing/internal/balancer/balancer.go`) uses linear gradient scoring for CPU, RAM, bandwidth, and geo distance. The only hard cutoff is `BWAvailable == 0` — a node at 95% CPU still receives traffic, just with a very low score.

Prometheus is configured (`infrastructure/prometheus/prometheus.yml`) with scrape targets for all services, but `alertmanagers.targets` is empty. No alert rules files exist anywhere in the infrastructure directory.

There is no cluster-wide utilization reporting beyond per-edge telemetry within Foghorn's internal state.

Evidence:

- `api_balancing/internal/balancer/balancer.go`
- `infrastructure/prometheus/prometheus.yml`

## Problem / Motivation

Gradient-only scoring means there is no floor — saturated nodes still receive traffic, just less of it. With few edges, even a low score can result in meaningful traffic to an overloaded node.

No alerting infrastructure means operators cannot detect saturation before it impacts viewers. There is no cluster-wide utilization visibility, so capacity planning is guesswork.

The N\*2 capacity model (never exceed 50% utilization across the cluster) is a recommended operational practice, but Foghorn has no mechanism to enforce, report, or even measure compliance with it.

## Goals

- Configurable per-resource exclusion thresholds (CPU, RAM, bandwidth) that remove nodes from routing when exceeded.
- Baseline Prometheus alert rules shipped as part of the platform.
- Cluster-wide capacity reporting: aggregate utilization vs total capacity across all edges.

## Non-Goals

- Autoscaling. Operators manage their own infrastructure; the platform reports, it does not provision.
- QoS tiers or SLA enforcement. Future marketplace feature, not in scope here.
- Predictive capacity planning or ML-based forecasting.

## Proposal

### Configurable utilization thresholds

Add per-resource exclusion thresholds to the balancer configuration via environment variables or cluster manifest fields. Examples: `BALANCER_CPU_THRESHOLD=85`, `BALANCER_RAM_THRESHOLD=90`, `BALANCER_BW_THRESHOLD=80`. Nodes above the threshold return score 0 and are excluded from candidate selection.

Defaults: no exclusion (backwards compatible with current behavior). Operators opt in by setting thresholds.

Fallback behavior: when all nodes exceed the threshold for a given resource, revert to gradient scoring and emit a warning metric. The system degrades gracefully rather than refusing all traffic.

### Prometheus alert rules

Ship as `infrastructure/prometheus/rules/frameworks.yml`. Baseline rules:

- Edge CPU saturation warning (>80%) and critical (>95%).
- Edge RAM saturation warning (>80%) and critical (>95%).
- Edge bandwidth saturation warning (>80%) and critical (>95%).
- Federation peer disconnection (peer not seen for >5 minutes).
- Stream replication failure (requested replica count not met).
- TLS certificate expiry (<30 days warning, <7 days critical).
- Kafka consumer lag exceeding threshold.
- Foghorn leader election churn (>3 elections in 10 minutes).

### N\*2 capacity reporting

Foghorn aggregates cluster-wide utilization: total CPU/RAM/BW used vs total CPU/RAM/BW available across all reporting edges. Exposed as Prometheus gauges (`frameworks_cluster_cpu_utilization_ratio`, etc.) and optionally via a Foghorn API endpoint.

Operators see aggregate numbers in their dashboards: "cluster is at 47% CPU, 32% RAM, 61% BW." A Prometheus alert fires when any resource exceeds 50% cluster-wide, aligning with the N\*2 recommendation.

## Impact / Dependencies

- `api_balancing/internal/balancer/` — threshold logic and cluster-wide aggregation.
- `infrastructure/prometheus/` — new `rules/` directory and alert rules file.
- `pkg/proto` — no changes needed; existing EdgeTelemetry already carries CPU/RAM/BW.
- Operator documentation — document threshold configuration and alert tuning.
- `docs/architecture/viewer-routing.md` — update to reflect threshold behavior.

## Alternatives Considered

- **Keep gradient-only scoring (status quo).** Works but provides no safety net. A single edge at 99% CPU still gets traffic.
- **Hard-code thresholds.** Inflexible for operators with different SLA requirements or cluster sizes.
- **Rely on external monitoring (Datadog, Grafana Cloud, etc.) for alerting.** Adds vendor dependency. Operators on sovereign infrastructure may not have these services.

## Risks & Mitigations

- **Aggressive thresholds with few edges could reject all nodes.** Mitigation: fallback to gradient scoring when all nodes exceed threshold, with a warning metric for operator visibility.
- **Alert fatigue from poorly tuned thresholds.** Mitigation: conservative defaults. Ship rules with sensible thresholds and document how to adjust them.
- **Cluster-wide aggregation in Foghorn adds state.** Mitigation: computed from existing per-edge telemetry that Foghorn already tracks. No new data collection needed.

## Migration / Rollout

1. **Prometheus alert rules.** No code change required — add configuration files to `infrastructure/prometheus/rules/`. Operators can adopt immediately.
2. **Configurable thresholds in balancer.** Small code change to scoring logic, with tests. Feature is opt-in via configuration; no behavior change at default settings.
3. **Cluster-wide capacity reporting.** New Prometheus metrics from Foghorn. Dashboard integration in Foredeck/Chartroom.

## Open Questions

- Should thresholds be per-cluster or global across federated clusters?
- Should the N\*2 warning live in Prometheus alerts, in the operator dashboard, or both?
- How do thresholds interact with cross-cluster federation? If local edges are all above threshold, should Foghorn route to a remote cluster's edges?

## References, Sources & Evidence

- [Evidence] `api_balancing/internal/balancer/balancer.go` (gradient scoring, BWAvailable == 0 cutoff)
- [Evidence] `infrastructure/prometheus/prometheus.yml` (empty alertmanagers, no rules)
- [Reference] `docs/architecture/viewer-routing.md`

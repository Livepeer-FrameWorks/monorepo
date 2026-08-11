# RFC: Workload Cost Model

## Status

Draft

## TL;DR

- Processing dispatch today routes by node class and in-flight count only; the actual resource cost of a job is content-dependent and only knowable after the fact, and GPU/VRAM is not measured anywhere in the platform.
- Introduce a predictive per-job cost model: supply-side GPU/VRAM telemetry (Helmsman measures, EdgeTelemetry contract extended), a demand-side predictor mapping job parameters (codec, resolution, framerate, model size, realtime factor) to an expected per-resource footprint, and a measured-feedback loop that corrects predictions from completed-job actuals.
- Add an interference term for co-placed jobs (GPU sharing, memory bandwidth, encoder contention with live serving), and feed the model into job dispatch admission/placement and capacity thresholds.
- Close the loop on the consumer side: continuous evaluation of measured utilization and measured network performance (supplied by the NAT-traversal RFC's probe infrastructure) translates into automatic server-selection/routing adjustment during live sessions, with hysteresis so short-lived fluctuations do not cause flapping between servers.

## Current State

Foghorn's processing dispatcher routes jobs by processing class and in-flight count, nothing more. `api_balancing/internal/jobs/job_router.go` resolves every queued job to the single class `video_transcode` (the `processing_jobs` table has no per-job class column), filters to alive, healthy, processing-capable nodes advertising that class, and picks the node with the fewest in-flight jobs of the class (`ClassLoad`). A preferred-node fast path exists for jobs pinned to a source node. Two jobs of the same class are treated as interchangeable regardless of whether one is a 240p clip trim and the other a 4K60 DVR export.

The capacity contract is slot-based. `ProcessingClassCapacity` (`pkg/proto/ipc.proto`) carries `slots_total`/`slots_used` per class (`video_transcode`, `ai_inference`, `cpu_heavy`) plus a class-specific `ready` list (e.g. loaded model ids for inference). Slots are opaque units: nothing relates a slot to CPU cores, VRAM, or encoder sessions, and slot ceilings are operator-configured guesses.

Node telemetry carries CPU, RAM, disk, shm, and bandwidth only. `NodeLifecycleUpdate` (Helmsman → Foghorn) and the cross-cluster `EdgeTelemetry`/`EdgeSnapshot` messages (`pkg/proto/foghorn_federation.proto`) have no GPU fields. Helmsman's hardware detection does not probe GPUs; no component in the platform measures GPU utilization, VRAM occupancy, or hardware encoder/decoder session counts. A node with a saturated GPU but idle CPU looks healthy to the dispatcher. Inter-server network performance is equally unmeasured: no component measures latency or throughput between platform servers (`docs/rfcs/nat-traversal.md` proposes the probe infrastructure that would supply it).

Consequences observed in the current design:

- Job cost is only observable after the fact (wall-clock duration, and CPU indirectly via node-level telemetry), and even then it is not attributed back to the job or retained for future placement decisions.
- Co-placed jobs interfere in ways the dispatcher cannot see: GPU sharing between transcode jobs, memory-bandwidth pressure, and hardware-encoder contention between processing jobs and live serving on the same node (transcode and DVR/clip work share nodes with viewer-facing delivery).
- `docs/rfcs/capacity-planning.md` scopes itself to reactive thresholds and explicitly lists predictive capacity planning as a Non-Goal; that Non-Goal now redirects here. Its threshold and N×2 reporting proposals cover CPU/RAM/bandwidth only — GPU is absent because the telemetry does not exist.
- `docs/rfcs/processing-orchestration.md` names capacity- and GPU-aware scheduling as the open grey area blocking processing from becoming a first-class orchestrated capability. This RFC is the design for that grey area.

The feature registry tracks this as `workload-cost-model` (`docs/platform-features.yaml`, area `processing`, kind `foundation`, status `roadmap`), which points at this RFC.

Evidence:

- `api_balancing/internal/jobs/job_router.go` (single class, lowest in-flight)
- `api_balancing/internal/jobs/processing_dispatcher.go`
- `api_balancing/internal/state/stream_state.go` (`CanRunClass`, `ClassLoad`)
- `pkg/proto/ipc.proto` (`ProcessingClassCapacity`, `NodeLifecycleUpdate`, `NodeLimits`)
- `pkg/proto/foghorn_federation.proto` (`EdgeTelemetry`, `EdgeSnapshot` — CPU/RAM/BW only)
- `pkg/mist/processes.go` (processing class constants)

## Problem / Motivation

Slot counting is the wrong granularity for heterogeneous processing work. A `video_transcode` slot occupied by a short 720p clip and one occupied by a multi-hour 4K DVR export consume wildly different CPU, VRAM, and encoder resources, yet count identically against `slots_total`. Operators must configure slot ceilings pessimistically (sized for the worst-case job) or risk oversubscription; both waste capacity.

GPU blindness makes this worse. GPU-equipped nodes are the platform's most expensive capacity, and the dispatcher cannot distinguish a node with 20 GB of free VRAM from one about to OOM. As AI inference and native transcode paths grow (`ai_inference` class, model loading, batch VOD work per the processing-orchestration RFC), placing jobs without VRAM accounting will produce hard failures (CUDA OOM) rather than graceful degradation.

Interference is invisible. Processing runs on nodes that also serve viewers. A transcode job that saturates the hardware encoder or memory bandwidth degrades live delivery on the same node, and the balancer's viewer-routing scores react only after viewers are already affected. Admission decisions need to account for what a job will do to its neighbors before it starts.

Finally, cost is content-dependent: the same job type varies by codec, resolution, framerate, and (for inference) model size. No static table will be accurate. Predictions must be corrected by measurement — the platform completes thousands of jobs whose actual costs are currently discarded.

## Goals

- GPU and VRAM become first-class node resources: measured by Helmsman, carried in the telemetry contract, visible to Foghorn's dispatcher and to capacity reporting.
- A demand-side cost predictor: given job parameters (codec, resolution, framerate, model size, realtime factor), produce an expected footprint per resource (CPU, GPU compute, VRAM, encoder sessions, memory bandwidth class, disk/network I/O).
- A measured-feedback loop: Helmsman reports per-job actuals on completion; Foghorn corrects predictions so the model converges on observed reality per node class and content bucket.
- An interference term: admission accounts for contention between co-placed jobs and between processing and live serving on shared nodes.
- Consumers wired in: job dispatch uses predicted-fit admission and placement scoring instead of raw in-flight counts; capacity thresholds and N×2 reporting (capacity-planning RFC) gain the GPU dimension and a predicted-committed view.

## Non-Goals

- Autoscaling or provisioning. The model informs placement and reporting; operators still own infrastructure (consistent with capacity-planning.md).
- Monetary cost, pricing, or billing changes. "Cost" here is resource footprint; rating and settlement are Purser's domain and unchanged.
- Optimal bin-packing or ML-based forecasting. The predictor is a parametric model with measured correction, not a learned scheduler. Sophistication can grow later behind the same interfaces.
- Orchestrating live transcode as jobs. Live processing policy remains the processing-orchestration RFC's scope; this RFC supplies the cost/telemetry substrate it will consume.
- Preemption or migration of running jobs. Admission-time decisions only.

## Proposal

### Phase 1: Supply-side GPU/VRAM telemetry

Extend the telemetry contract in `pkg/proto` with a per-device GPU message, reported by Helmsman:

- `GpuTelemetry`: device index, vendor/model string, `vram_total_bytes`, `vram_used_bytes`, `gpu_utilization_percent`, `encoder_sessions_used`/`encoder_sessions_max`, `decoder_utilization_percent`. Repeated field on `NodeLifecycleUpdate` (the Helmsman → Foghorn path) and, in smoothed form, on `EdgeSnapshot`/`EdgeTelemetry` (the cross-cluster scoring path) so federation-aware placement sees the same picture.
- Helmsman measurement: extend hardware detection to enumerate GPUs at startup and sample utilization on the existing telemetry cadence (NVML for NVIDIA first; the proto stays vendor-neutral). Nodes without GPUs report an empty list — zero behavioral change.
- Foghorn state: `NodeState` gains GPU fields alongside CPU/RAM; exposed as Prometheus gauges per `docs/standards/metrics.md` naming.

This phase is independently useful (operator visibility into GPU fleet) and is a prerequisite for everything below.

### Phase 2: Demand-side cost predictor

A cost vector and a parametric predictor in Foghorn:

- Cost vector: `{cpu_millicores, gpu_percent, vram_bytes, encoder_sessions, membw_class, disk_iops_class, net_bytes_per_sec}`. Coarse classes (low/medium/high) for dimensions that resist precise prediction (memory bandwidth, disk I/O); numeric estimates where parameters allow it (VRAM from resolution + codec + model size).
- Predictor input: job parameters already known at enqueue time — job kind (clip, VOD transcode, DVR chapter, inference), source codec and resolution, target renditions, framerate, expected realtime factor, and for inference the model id/size. `processing_jobs` gains a `class` column (fixing the single-class limitation noted in job_router.go) and a parameters snapshot sufficient to evaluate the predictor.
- Baseline: a static table shipped with Foghorn (per codec/resolution/fps bucket), deliberately conservative. Accuracy comes from Phase 3, not from tuning the table.
- Admission: a node is eligible when predicted footprint fits within measured headroom minus committed cost of already-placed jobs (Foghorn tracks the sum of predicted vectors of in-flight jobs per node). Placement prefers the eligible node with the most post-placement headroom on the scarcest dimension, replacing lowest-in-flight as the tiebreak. Slot ceilings remain as a coarse upper bound during transition.

### Phase 3: Measured-feedback loop

- Helmsman samples per-job actuals: peak/mean CPU, peak VRAM, encoder-session occupancy, wall-clock vs media duration (realtime factor), attributed via the job's process/cgroup where available and reported in the existing job completion result message (`ipc.proto` processing result extended with an actuals block).
- Foghorn persists actuals against the job row and maintains per-(class, parameter-bucket, node-hardware-profile) correction factors — an EWMA of actual/predicted ratios stored in the `foghorn` schema. Predictions are baseline × correction. Buckets with insufficient samples fall back to parent buckets, then to the static baseline.
- Divergence visibility: a metric for prediction error per bucket, so operators (and this RFC's authors) can see where the model is wrong before trusting it for tighter admission.

### Phase 4: Interference term

- Co-placement penalty: when scoring a candidate node, inflate the predicted footprint of the incoming job by an interference factor derived from what already runs there — GPU-sharing penalty when another GPU job is resident, encoder contention when hardware encoder sessions approach the device maximum, memory-bandwidth penalty when co-resident jobs are membw-heavy.
- Live-serving protection: nodes serving viewers reserve headroom for the media plane. Admission subtracts a configurable serving reserve (CPU, encoder sessions, bandwidth) scaled by current viewer load before evaluating fit, so processing cannot starve delivery on shared nodes.
- Feedback: Phase 3 actuals are recorded with a co-placement context (what else ran during the job), allowing interference factors to be corrected from measurement rather than remaining hand-tuned constants.

### Phase 5: Consumers

- Job dispatch: `routeProcessingJob` evolves from class + in-flight count to class + predicted-fit admission + headroom scoring (Phases 2–4). Behavior is flag-gated; the legacy path remains until prediction-error metrics justify cutover.
- Capacity thresholds and reporting: the capacity-planning RFC's exclusion thresholds and N×2 cluster reporting gain the GPU dimension (utilization and VRAM) and a committed-vs-measured view — cluster capacity dashboards can show both what is measured now and what dispatch has already promised. That RFC's Non-Goals section already redirects predictive scope here.
- Placement policy engine: the cost model becomes an available decision input at the `process` verb per `docs/rfcs/placement-policy-engine.md`; no policy semantics are defined here.
- Continuous routing adjustment during live sessions: the measured inputs the model maintains are evaluated continuously, not only at admission time. Node utilization actuals, and — once the NAT-traversal RFC's probe infrastructure exists (`docs/rfcs/nat-traversal.md`) — measured inter-server network performance (latency, throughput), translate into automatic server-selection adjustment on the delivery side: scoring steers new sessions away from nodes and paths under sustained degradation and rebalances load toward healthier capacity while streams stay live. Adjustment applies hysteresis: a signal must hold beyond its bound for a dwell window before it changes selection, and re-inclusion uses a wider band than exclusion, so short-lived fluctuations do not cause flapping between servers. This layer sits above the capacity-planning RFC's exclusion thresholds (`docs/rfcs/capacity-planning.md`), which remain the reactive guard rail that hard-excludes saturated nodes; the adjustment logic acts earlier and gradually so the guard rail rarely trips. It concerns viewer/session routing only — processing-job placement remains admission-time, per Non-Goals.

## Impact / Dependencies

### Owning services / modules

- **Foghorn** (`api_balancing`) — owns the cost model: predictor, correction store, committed-cost tracking, admission/placement in the job router and processing dispatcher, and cluster-wide GPU capacity aggregation.
- **Helmsman** (`api_sidecar`) — owns measurement: GPU enumeration and sampling, per-job actuals capture, reporting via node lifecycle updates and job results.
- **pkg/proto** — owns the telemetry contract: `GpuTelemetry`, `NodeLifecycleUpdate`/`EdgeSnapshot`/`EdgeTelemetry` extensions, processing result actuals block.

### Other impact

- `pkg/database/sql/schema/foghorn.sql` — `processing_jobs` class + parameters columns, job actuals, correction-factor table (additive migrations per `docs/standards/schema-migrations.md`).
- `infrastructure/prometheus/` — GPU gauges and prediction-error metrics join the capacity-planning rule set.
- `docs/rfcs/capacity-planning.md` — its Non-Goals redirect predictive scope here; GPU dimension lands there once Phase 1 telemetry exists.
- `docs/rfcs/processing-orchestration.md` — its GPU-aware scheduling grey area resolves by reference to this RFC.
- `docs/platform-features.yaml` — `workload-cost-model` status advances as phases land.
- No GraphQL/webapp surface: this is a scheduling foundation; value surfaces through processing and analytics (per the registry entry).

## Alternatives Considered

- **Status quo (slots + in-flight count).** Fails as soon as job sizes diverge or GPUs matter; slot ceilings must be sized for worst-case jobs, wasting capacity.
- **Finer-grained static classes** (e.g. `video_transcode_4k`, `video_transcode_sd`). Postpones the problem: still no VRAM accounting, no interference, and class proliferation without measurement.
- **Kubernetes-style resource requests declared per job by the caller.** Callers (Commodore, upload pipeline) do not know job cost either; the knowledge lives in measured history, which is exactly what the feedback loop captures.
- **Full ML-based cost prediction.** Data volume and operational complexity are not justified before a parametric model with measured correction has been tried; the interfaces here (cost vector, correction store) admit an ML predictor later without contract changes.
- **Measure-only, no prediction (admit optimistically, evict on pressure).** Eviction mid-transcode wastes more work than conservative admission, and GPU OOM is a hard failure, not a pressure signal.

## Risks & Mitigations

- **Bad predictions cause under-utilization (too conservative) or overload (too optimistic).** Mitigation: flag-gated cutover, legacy routing retained; prediction-error metrics observed per bucket before tightening admission; slot ceilings kept as an outer bound during transition.
- **Vendor-specific GPU measurement (NVML) limits coverage.** Mitigation: vendor-neutral proto; nodes without supported GPUs simply omit the fields and are treated as GPU-less, which matches today's behavior.
- **Per-job attribution is imprecise** (shared processes, Mist-driven pushes not cleanly cgrouped). Mitigation: coarse membw/IO classes instead of false precision; attribute what is attributable (VRAM, encoder sessions, process CPU) and mark the rest estimated.
- **Correction store adds state to Foghorn.** Mitigation: correction factors are advisory and reconstructible from retained actuals; loss degrades to the static baseline, not to failure. Fits the existing Foghorn HA model (Redis/Postgres-backed state).
- **Interference factors are initially hand-tuned.** Mitigation: recorded co-placement context makes them measurable over time; until then they only add conservatism, never admit more than the non-interference model would.

## Migration / Rollout

1. **Phase 1** ships alone: proto extension, Helmsman GPU sampling, Foghorn state + metrics. Observability-only; no dispatch behavior change. Requires a Helmsman/Foghorn release pairing (proto is backwards-compatible; absent fields mean no GPU).
2. **Phase 2** behind a dispatch flag: predictor and committed-cost tracking run in shadow mode first (log the decision the model would have made alongside the legacy decision), then cut over per cluster.
3. **Phase 3** actuals reporting lands with the next Helmsman release; correction factors accumulate before they gate anything.
4. **Phase 4** interference term enabled after Phase 3 provides co-placement data; serving reserve defaults conservative.
5. **Phase 5** consumer wiring (capacity thresholds, N×2 GPU reporting) follows the capacity-planning RFC's own rollout once telemetry exists.

Each phase is independently revertible via flags; schema changes are additive.

## Open Questions

- Should the correction store be per hardware profile (GPU model + CPU class) or per node? Per-profile converges faster but assumes profile detection is reliable.
- How should `ai_inference` model residency interact with VRAM accounting — is a loaded-but-idle model committed VRAM, or reclaimable on demand?
- Does the cross-cluster path need the full cost model, or is a coarse per-cluster GPU headroom summary sufficient for federation-level placement?
- Where does the serving reserve live: Foghorn config, cluster manifest, or the placement policy engine's `process` verb once that lands?
- Should completed-job actuals also flow to the data plane (Decklog/Periscope) for operator analytics, or remain Foghorn-internal scheduling state?
- How fast should the continuous-adjustment layer react without destabilizing active streams? Aggressive reaction chases transients and causes viewer-visible churn; slow reaction lets degradation persist. Dwell windows and hysteresis band widths likely need per-signal tuning (utilization vs. network-path measurements), and the right values are a measurement question that cannot be settled before both signals exist in production.

## References, Sources & Evidence

- [Evidence] `api_balancing/internal/jobs/job_router.go` — single `video_transcode` class, lowest in-flight routing
- [Evidence] `pkg/proto/ipc.proto` — `ProcessingClassCapacity` (slot model), `NodeLifecycleUpdate` (no GPU fields)
- [Evidence] `pkg/proto/foghorn_federation.proto` — `EdgeTelemetry`/`EdgeSnapshot` (CPU/RAM/BW only)
- [Evidence] `pkg/mist/processes.go` — processing class constants
- [Reference] `docs/rfcs/capacity-planning.md` — reactive thresholds and N×2 reporting; its predictive Non-Goal redirects here, and its exclusion thresholds are the guard-rail layer under the continuous-adjustment consumer
- [Reference] `docs/rfcs/nat-traversal.md` — probe infrastructure supplying the inter-server network-performance signal the continuous-adjustment consumer evaluates
- [Reference] `docs/rfcs/processing-orchestration.md` — GPU-aware scheduling named as the open grey area this RFC designs
- [Reference] `docs/rfcs/placement-policy-engine.md` — cost model as decision input at the `process` verb
- [Reference] `docs/platform-features.yaml` — registry item `workload-cost-model` (processing, foundation, roadmap)
- [Reference] `docs/architecture/processing-pipeline.md` — the implemented artifact job pipeline this model schedules

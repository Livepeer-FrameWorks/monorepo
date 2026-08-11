# RFC: External Player QoE Telemetry

## Status

Draft

## TL;DR

- Per-viewer QoE today exists only for the first-party FrameWorks player (boot waterfall, additive-delta session QoE, VOD retention). Sessions played through hls.js, Video.js, native apps, or any CMCD-capable client are invisible to `/analytics/qoe`.
- Proposal: an ingestion + normalization layer — Bridge accepts heterogeneous telemetry (a documented external beacon schema for SDK-less integrations, plus CMCD where players already emit it); Periscope normalizes everything to the **same metric definitions** the first-party player uses (rebuffering ratio, TTFF, EBVS, retention) and stores it in the existing session-delta tables with an explicit source dimension.
- Metrics that a given source can only approximate are **marked, never blended**: per-metric capability flags and per-source segmentation on read surfaces, so sources that report less never silently look better.
- The trust boundary does not move: Bridge keeps minting identity server-side (Commodore attribution, canonical `event_id`), QoE stays diagnostic-only (no billing/viewer-count authority), and edge attribution stays gated on the signed telemetry token.

## Current State

The per-viewer QoE pipeline is live end-to-end for the first-party player only (`docs/architecture/analytics-pipeline.md`, "Player boot telemetry" and "Viewer-experienced QoE"):

- **Boot telemetry** — the npm player records a boot waterfall (`gateway_resolve`, `mist_hydrate`, `player_select`, `connect`, `prebuffer`, first painted frame) plus Resource Timing, and posts an opt-in one-shot beacon to Bridge at `POST /playback/telemetry/boot` (`api_gateway/internal/handlers/playback_telemetry.go`). Rows land in `player_boot_samples`.
- **Session QoE** — the player's `SessionQoeReporter` emits additive-delta beacons (~30s heartbeats + final on `pagehide`) to `POST /playback/telemetry/session` (`api_gateway/internal/handlers/playback_session_telemetry.go`). Rows land in `client_qoe_session_deltas` (ReplacingMergeTree deduped on the client-stable `(tenant_id, content_id, session_id, beacon_seq)`); the VOD retention histogram rides the same beacon into `vod_retention_buckets`.
- **Shared trust boundary** — both handlers build on `BeaconIntake` (`api_gateway/internal/handlers/beacon_intake.go`): the browser supplies only `content_id` + ephemeral session identifiers; Bridge resolves ownership through Commodore (tenant/stream/artifact/origin cluster), mints the canonical UUIDv7 `event_id`, rate-limits per IP and per `(IP, session)`, caps and redacts bodies, and trusts serving `node_id`/`serving_cluster_id` only from the signed telemetry token minted at `resolveViewerEndpoint` (`pkg/telemetrytoken`). Untokened rows get `cluster_attributed = 0` and are excluded from cluster-ops reads.
- **Metric definitions** are deliberate and non-obvious: rebuffering ratio counts only genuine post-first-frame stalls over genuine watch time (union of `video.played`, never wall-clock); EBVS is `play_intent ∧ ¬first_frame`; frame stats are per-window deltas guarded by `frame_stats_supported`; VOD retention derives two independent curves (density from `vod_retention_buckets`, reach from `max_bucket_reached`).
- **Read surfaces** — tenant (`analytics.health.sessionQoeSummary` / `.playerBootSummary` / `.vodRetention`) and cluster-ops (`analytics.infra.clusterQoeOps` / `.clusterBootOps`, token-attributed rows only), surfaced at `/analytics/qoe`.

**The gap:** every one of these signals originates in the npm player's instrumentation. A tenant serving the same streams through hls.js on their own site, a Video.js embed, a native mobile/TV app, or a player that already emits CMCD gets server-side stream health only — no per-viewer rebuffering, no TTFF, no EBVS, no retention. The registry already names this: the `analytics` item's `gap_reason` lists "QoE from non-FrameWorks / third-party players" as still maturing (`docs/platform-features.yaml`).

## Problem / Motivation

- Many production integrations do not use the FrameWorks player. For those tenants the QoE dashboard is empty or — worse — quietly describes only the subset of viewers on the first-party player, presented as if it were the whole audience.
- Server-side signals cannot substitute: for HLS/DASH the player prefetches, so origin buffer state reads healthy while a viewer's last-mile link stalls. Per-viewer rebuffering is only measurable at the client. Without an intake path for external players, that measurement is structurally impossible for most of the audience.
- CMCD exists precisely so players can report this without a proprietary SDK; several mainstream players (hls.js, dash.js, Shaka) can emit it today. We currently discard it.
- If each source were normalized ad hoc, metric definitions would drift (e.g., a rebuffering ratio over wall-clock vs. genuine watch time) and cross-source comparisons would be meaningless. Normalization must be centralized and definitional parity enforced.

## Goals

- Accept per-viewer QoE telemetry from players we do not ship: a documented, versioned external beacon schema (SDK-less HTTP integration) and CMCD for players that already emit it.
- Normalize every source to the **same metric definitions** as the first-party player: rebuffering ratio, TTFF, EBVS, mid-stream failure, bitrate/ABR, VOD retention — one definition per metric, owned in one place (Periscope ingest).
- Make approximation explicit: per-row source labeling plus per-metric capability flags, so a metric a source cannot measure is absent/flagged, never zero-filled, and read surfaces never blend sources into a single unlabeled ratio.
- Preserve the existing trust boundary unchanged: server-side identity minting, server-derived attribution, no client-poisonable billing fields, edge attribution only via signed telemetry tokens.
- Keep integration cost low for customers: no mandatory SDK; a beacon POST or existing CMCD emission suffices.

## Non-Goals

- Changes to the first-party player SDKs (`npm_player`). Its pipeline is the reference implementation and stays as-is.
- Promoting any client-originated QoE to viewer-count or billing authority. External QoE inherits the diagnostic-only stance verbatim; `viewer_sessions_final` and canonical ledgers remain the billing truth.
- Shipping or maintaining per-player plugins (a hls.js plugin, a Video.js plugin, an ExoPlayer module). The contract is the beacon schema and CMCD; reference snippets in docs are fine, owned player-side code is not.
- Glass-to-glass latency measurement or QoE metrics beyond the established first-party set.
- Inferring per-viewer QoE from edge request logs alone (segment timing heuristics). Considered as a complement (see Open Questions), not a substitute for client-reported data.

## Proposal

One ingestion + normalization layer, reusing the existing beacon path end-to-end: source → Bridge (`BeaconIntake`) → Decklog → Periscope Ingest (normalization) → ClickHouse (existing tables + source dimension) → Periscope Query (source-aware reads).

Common design decisions across phases:

- **Same tables, new dimension.** External sessions land in `player_boot_samples` / `client_qoe_session_deltas` / `vod_retention_buckets` with a `telemetry_source` column (`player_sdk` | `external_beacon` | `cmcd`; existing rows backfill-default to `player_sdk`). No parallel table family — one metric definition implies one storage shape.
- **Per-metric capability flags, extending the existing pattern.** `frame_stats_supported` already encodes "0/0 is not perfect". The same pattern extends to what each source can measure: e.g. `rebuffer_supported`, `ttff_supported`, `retention_supported`. The normalizer sets these from the source type and payload contents; read-time ratios compute each metric only over rows where the metric is supported. This is the mechanism that prevents silent bias toward players that report less: a source that omits rebuffering simply contributes nothing to the rebuffering denominator, instead of contributing zeros.
- **Thin edge, central normalization.** Bridge stays syntax + trust only (validation, attribution, `event_id`, token verification, rate limits) — no metric math at the edge. Periscope Ingest owns all mapping from source payloads to canonical deltas, so definitional parity lives in exactly one service.
- **Trust boundary verbatim.** External payloads carry only `content_id` + session identifiers; Commodore resolution stamps tenant/stream/artifact/origin; Bridge mints `event_id`; serving node/cluster come only from a valid telemetry token (external integrations that resolve playback through the gateway receive one like the first-party player does; integrations that bypass resolution simply get `cluster_attributed = 0` rows, excluded from cluster-ops reads — same rule as today). Rate limiting reuses the per-`(IP, session)` budgets with the per-IP backstop.

### Phase 1: External beacon schema v1 (SDK-less integrations)

A documented, versioned JSON schema that any client able to issue an HTTP POST can implement, deliberately shaped as a strict subset-compatible sibling of the first-party session beacon: `contentId`, `sessionId`, `beaconSeq`/`isFinal`, additive delta counters (`playedMs`, `rebufferMs`, `rebufferCount`, frame deltas, `bitrateBpsSeconds`, ABR switches), `playIntent`/`firstFrame`, optional TTFF milliseconds (single scalar — external clients are not expected to reproduce the span waterfall), optional VOD retention histogram. Plus `telemetrySource: "external_beacon"` and a self-declared `playerType`/`playerVersion`.

- Served on new endpoints under the existing prefix (`/playback/telemetry/external/*` or a `source` discriminator on the existing routes — decided at implementation) through the shared `BeaconIntake`.
- Semantics documented normatively per metric (what counts as a genuine stall, that `playedMs` is the union of played ranges, delta-not-cumulative counters). Fields absent from a payload mark the corresponding `*_supported = 0` — a partial implementation degrades to fewer metrics, never to wrong ones.
- Dedup: same client-stable `(tenant_id, content_id, session_id, beacon_seq)` key; the schema makes `beaconSeq` mandatory.
- A single scalar TTFF lands as an approximated boot row (`player_boot_samples` with only totals populated, spans empty, capability-flagged) so tenant TTFF percentiles can include external players with the approximation visible.

### Phase 2: CMCD intake

Accept CMCD (CTA-5004) for players that already emit it, in **reporting/beacon form**: a Bridge endpoint receiving CMCD key-value payloads (CMCDv2 response/event-mode style JSON), keyed by CMCD `sid` → `session_id` and `cid` → `content_id`.

- Periscope Ingest gains a CMCD normalizer mapping to canonical semantics where the mapping is sound, and marking approximation where it is not:
  - `bs` (buffer starvation) → rebuffer count; stall duration approximated from starvation-flagged request spacing → `rebuffer_supported` conveys "approximate" for CMCD-only sessions.
  - `su` (startup) transition → TTFF approximation (time from first `su` request to first non-`su` request); no first-painted-frame signal exists in CMCD, so TTFF is capability-flagged as approximate.
  - `br`/`tb`/`mtp` → bitrate and delivery metrics (well-defined).
  - EBVS approximable only as "session with `su` requests and no steady-state requests"; retention not derivable from CMCDv1 (no playhead) — both flagged unsupported unless CMCDv2 playhead data is present.
- CMCD beacons have no client `beaconSeq`; the normalizer windows CMCD reports per `sid` and derives a monotonic sequence at ingest, accepting that replayed client reports dedup less precisely than schema-v1 beacons (Kafka replay is still covered by the Bridge-minted `event_id`).
- Request-attached CMCD (query args/headers on media requests hitting the edge) is explicitly out of scope for this phase — see Open Questions.

### Phase 3: Source-aware read surfaces

- `sessionQoeSummary` / `playerBootSummary` / `vodRetention` / `clusterQoeOps` gain a source breakdown and per-metric coverage counts ("rebuffering ratio computed over N of M sessions; excluded sources: …"). Blended single-number ratios across sources with different capability sets are not produced.
- `/analytics/qoe` labels approximate metrics visually and lets tenants segment by source/player type; the existing first-party-only view remains the default lens where sources diverge.
- The deferred 5m num/denom rollup (`analytics-pipeline.md`), when volume forces it, must group by `telemetry_source` and respect capability flags — recorded here so the rollup design does not accidentally re-blend sources.

## Impact / Dependencies

- **Bridge (`api_gateway`)** — new/extended beacon endpoints on the shared `BeaconIntake`; no changes to trust logic beyond routing new payload shapes through it.
- **Decklog (`api_firehose`)** — new trigger/event types for external beacon + CMCD payloads (pass-through, consistent with existing gateway telemetry ingest).
- **Periscope Ingest (`api_analytics_ingest`)** — the normalization layer: two new processors (external beacon, CMCD) emitting rows under canonical metric definitions; owns the capability-flag decisions.
- **Periscope Query (`api_analytics_query`)** — source-aware summaries and coverage annotation on the existing QoE RPCs.
- **ClickHouse (`pkg/database/sql/clickhouse`)** — additive columns on `player_boot_samples` / `client_qoe_session_deltas` (`telemetry_source`, per-metric `*_supported` flags); no table splits, no backfill beyond defaults.
- **GraphQL / webapp** — schema additions for source breakdown; `/analytics/qoe` labeling. Codegen per standard flow.
- **Docs** — public integration doc for the beacon schema and CMCD support (`website_docs`); `analytics-pipeline.md` gains the external-source section on implementation.
- **Registry** — consolidation, not a new item: the `analytics` item's `gap_reason` already names third-party player QoE; on implementation its prose moves from "still maturing" to shipped scope. The roadmap.mdx "View-Level QoE Analytics" entry is the corresponding roadmap home. No new slug.

### Owning services / modules

- **Bridge (`api_gateway`) — ingest edge.** Owns the public intake surface: endpoint shapes, rate limiting, server-side attribution and `event_id` minting, telemetry-token verification. The trust boundary continues to live in exactly one place (`BeaconIntake`).
- **Periscope (`api_analytics_ingest` + `api_analytics_query`) — normalization + storage + reads.** Ingest owns source→canonical metric mapping and capability flagging; Query owns source-aware aggregation. ClickHouse schema changes ride Periscope's migration stream.
- **Player SDKs (`npm_player`) — unaffected.** The first-party pipeline is unchanged and serves as the reference semantics for the normalizer.

## Alternatives Considered

- **Per-source parallel tables** (e.g., `cmcd_sessions`) — rejected: splits every read surface, invites per-source metric drift, and makes "no silent bias" a query-discipline problem instead of a schema property.
- **Edge-side CMCD harvesting only** (parse request-attached CMCD from Caddy/Mist logs, no new endpoints) — insufficient alone: request-mode CMCD sees only sessions routed through our edge (not tenant CDNs), has no first-frame/played-time signals, and couples QoE to media-plane log plumbing. Retained as a possible complement (Open Questions).
- **Shipping official third-party player plugins** — rejected as owned scope: an unbounded maintenance surface across player ecosystems. The versioned beacon schema puts the thin integration on the customer side; docs may carry reference snippets.
- **Adopting a third-party analytics vendor schema wholesale** — rejected: our metric definitions (genuine-stall rebuffering, played-union denominators, EBVS, dual retention curves) are already deliberate and live; normalizing inbound data to them is cheaper than re-deriving every dashboard and RPC against a foreign model. CMCD is the standards-track input format, not the storage model.

## Risks & Mitigations

- **Approximations read as measurements.** A CMCD-derived TTFF is not a painted-frame TTFF. Mitigation: capability flags are load-bearing (reads filter on them), read surfaces annotate coverage and approximation, and no blended cross-source single numbers are produced.
- **Open-endpoint abuse / data poisoning.** Anyone holding a public `content_id` can post plausible-looking QoE. Mitigations, unchanged in kind from today: per-IP and per-`(IP, session)` rate limits with drop-not-queue (204), body caps, attribution-or-drop, URL redaction; cluster-level reputation is protected because `cluster_attributed` requires the signed token; source segmentation means fabricated `external_beacon` rows can never contaminate first-party metrics; and the diagnostic-only stance bounds blast radius — nothing here feeds billing or viewer counts.
- **Metric drift between normalizers.** Two processors re-implementing "genuine stall" would recreate the problem this RFC exists to solve. Mitigation: canonical definitions live once in Periscope Ingest with shared helpers; the external beacon schema documents the semantics normatively; parity asserted by fixture tests that run identical scenarios through the first-party and external paths.
- **CMCD implementation variance across players.** Players disagree on which keys they emit and when. Mitigation: normalize conservatively — unknown or ambiguous data lowers capability flags rather than producing guessed values.
- **Weaker dedup for CMCD sessions** (no client `beaconSeq`). Mitigation: ingest-derived sequencing per `sid` plus the Bridge-minted `event_id` for replay; accepted residual risk is double-counted client resends within a window, bounded and confined to the `cmcd` source segment.

## Migration / Rollout

1. **Schema expand** — additive ClickHouse columns with defaults (`telemetry_source = 'player_sdk'`, capability flags set true on existing first-party rows by default expression). No backfill, no contraction step.
2. **Phase 1 ship behind config** — external beacon endpoints gated on a Bridge config flag (mirroring `TELEMETRY_TOKEN_SECRET`-style optionality); normalizer + tests land in Periscope Ingest; docs published for design partners.
3. **Phase 2 ship** — CMCD endpoint + normalizer, initially validated against hls.js/dash.js emissions.
4. **Phase 3 ship** — source-aware read surfaces and dashboard labeling; registry `analytics` `gap_reason` and roadmap.mdx updated to reflect shipped scope.
5. **RFC lifecycle** — on implementation, content merges into `docs/architecture/analytics-pipeline.md` (external-source section) + `website_docs` integration guide, and this RFC is deleted per policy.

## Open Questions

- **Request-attached CMCD at the edge**: should Helmsman/Caddy additionally harvest query/header CMCD from media requests as a zero-integration complement (edge-visible sessions only)? If yes, it arrives via the trigger path, not Bridge — a scope decision for the media-plane boundary, deferred here.
- **Telemetry token distribution for external integrations**: today the token is minted at `resolveViewerEndpoint`. Should gateway-issued playback URLs optionally embed/return the token so SDK-less integrations can achieve `cluster_attributed = 1` without calling resolve themselves?
- Endpoint shape: new `/playback/telemetry/external/*` routes vs. a `source` discriminator on the existing routes (implementation-time decision; no semantic difference).
- Should external sessions correlate into any session-level joins beyond QoE reads (e.g., support tooling views), given their weaker identity guarantees?
- Minimum viable CMCD version stance: v1-only keys at Phase 2, or require v2 response-mode for retention/playhead support from the start?

## References, Sources & Evidence

- [Source] `docs/architecture/analytics-pipeline.md` — "Player boot telemetry", "Viewer-experienced QoE (session deltas)", "Client QoE sampling" (metric definitions, trust boundary, diagnostic-only stance).
- [Evidence] `api_gateway/internal/handlers/beacon_intake.go` — shared trust boundary: attribution via Commodore, `event_id` minting, telemetry-token verification, rate limits.
- [Evidence] `api_gateway/internal/handlers/playback_telemetry.go`, `api_gateway/internal/handlers/playback_session_telemetry.go`, `api_gateway/cmd/bridge/main.go` (route registration) — existing first-party beacon endpoints.
- [Evidence] `pkg/database/sql/clickhouse/periscope.sql` — `client_qoe_session_deltas` (client-stable dedup key, `frame_stats_supported` precedent), `player_boot_samples`, `vod_retention_buckets`.
- [Evidence] `docs/platform-features.yaml` — `analytics` item `gap_reason` naming third-party player QoE as the open gap (registry consolidation target).
- [Reference] `pkg/telemetrytoken` — signed telemetry token (edge attribution gate).
- [Reference] CTA-5004 Common Media Client Data (CMCD), including CMCDv2 reporting modes.
- [Reference] `PLAN_PLATFORM_MODULE_MAP.md` — RFC work queue item 4; Batch-3 RFC-NOTE anchoring this design at Bridge's beacon intake.

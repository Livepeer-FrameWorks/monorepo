# RFC: Upgrade Support Contract

## Status

Draft

## TL;DR

- FrameWorks does not state which release a cluster may upgrade from. The CLI refuses unsafe moves one check at a time. `release apply` now makes every refusal about earlier releases before its first mutation.
- Two contracts are compared: one release at a time (Proposal A), and a six-month upgrade window with a CLI walker that applies each intermediate release (Proposal B). Both build on the source-floor gate (`min_source_version` plus host detection), which exists now.
- Recommendation: adopt Proposal A in its cheap form. Its enforcement is built: the prior-release check runs in `release apply` preflight and the refusal names the next release to apply. What remains is the written promise. Revisit Proposal B when an operator needs a published window. A long-term-support line with backports stays out of scope.

## Owning services / modules

The CLI owns every part of this: the release catalog (`cli/internal/releases`), the upgrade and release commands (`cli/cmd/cluster_upgrade.go`, `cli/cmd/cluster_release.go`), the pre-deploy gate (`cli/cmd/cluster_upgrade_gate.go`), the source-floor gate (`cli/cmd/cluster_source_floor.go`), host detection (`cli/pkg/detect`), and schema verification (`cli/pkg/provisioner`, `make verify-schema-migrations`). The release workflow (`.github/workflows/release.yml`) publishes the catalog-derived release metadata. No service changes.

## Current State

**What a release can declare.** Each entry in `cli/internal/releases/catalog.yaml` can carry `min_cli_version`, `rollback_disabled`, `required_data_migrations`, and `min_source_version`. Release transitions are declared beside the releases. `schema_migration_floor` (v0.3.0) marks where migrations were folded into the baselines. The CLI embeds the catalog, and `release-metadata` copies the per-release fields into the published release manifest. The CLI refuses a fetched manifest whose transitions or source floor disagree with its own catalog.

**How a move is checked today.**

- `release apply` and `cluster upgrade` resolve a target and deploy it directly. Nothing walks intermediate releases.
- The source-floor gate (`enforceSourceFloor`, `cli/cmd/cluster_source_floor.go`) detects the release of every platform-artifact replica over SSH. Before any mutation it refuses a replica below the target's effective `min_source_version`, a rollback below the floor of a running release, and a replica whose version is not a release. The effective floor carries forward from earlier releases. No release declares a floor yet, so the gate reads nothing today. The first user is the release after v0.3.11 (see Migration / Rollout).
- `release apply` checks prior-release completeness in its preflight, after the source floor and before the first mutation, including under `--dry-run` (`enforcePriorReleasesComplete`, `cli/cmd/cluster_prior_release_preflight.go`). It runs `firstIncompletePriorRelease` over the postdeploy ledger of every PostgreSQL/YugabyteDB service database that already has a baseline or ledger and over the ClickHouse ledgers, and checks the required data migrations of earlier releases (`datamigrate.PreDeployBlockers`). The refusal names the lowest unfinished release and says to apply it first with `release apply --version`. This is Proposal A, enforcement option 1, below.
- The per-service pre-deploy gate (`runUpgradePreDeployGate`) still runs for every deployed service as a second line of defence, and it is the only prior-release check `cluster upgrade` has. Every expand migration up to the target must be applied, and so must every postdeploy migration of every earlier release; required data migrations of earlier releases are checked the same way.
- Across the v0.3.0 lifecycle boundary, only the republished v0.2.96 is a valid source (`cli/pkg/provisioner/migration_floor_guard.go`, catalog `schema_migration_sentinels`).
- `make verify-schema-migrations` replays the schema from one base: the latest tag's baseline plus the pending migrations must converge to the current baseline.
- Rollout readiness depends on the release: from v0.3.11, Go services serve `/ready`. Rollout gates choose the probe path from the release they deploy (`servicedefs.ReadinessPathFor`). Consumers that cannot see the release (Quartermaster, Navigator, doctor) stay on `/health` until a catalog floor guarantees every running binary serves `/ready`. See `docs/architecture/cluster-rollout.md`, "Readiness contract".

**Cadence.** The repository has 122 tags since 2025-11-13. They come in bursts: 56 in May 2026, 19 in June, one in August, and v0.3.0 through v0.3.10 between 2026-09-14 and 2026-09-18. In the v0.3 line, schema migrations ship in v0.3.0, v0.3.3, v0.3.7, v0.3.8, v0.3.9, v0.3.10 and the pending v0.3.11. Required data migrations ship in v0.3.0, v0.3.8 and v0.3.11. `rollback_disabled` is set in v0.3.0 and v0.3.11. Most releases are therefore gated.

## Problem / Motivation

- Operators cannot tell in advance whether a cluster several releases behind can move straight to the latest release. They find out when a gate refuses. `release apply` refuses in preflight, but `cluster upgrade` still refuses per service, after earlier services in the same run have moved.
- Required data migrations run through the binary of the release that introduced them. A skipped release's data migration cannot run without that release's image, so a skip is only recoverable by going back to apply it.
- The PLAN decision (P12, 2026-09-15) was to write down both possible contracts and decide later. The window length to evaluate is six months. Something has to be enforced eventually, because the catalog already records per-release floors and nothing states what operators may rely on.

## Goals

- One written contract for which source releases a target accepts, enforced by the CLI before the first mutation.
- A refusal that names the release to apply next.
- No stored "applied release" record. The source comes from host detection and the migration ledger (see the no-durable-release-record rule).
- No override flags on safety floors.

## Non-Goals

- A long-term-support line with backports. It needs maintenance branches or cherry-picks, a new version scheme (patch numbers already carry features), and tags alongside newer releases. That last part breaks the one-pending-release rule and `make validate-migrations`.
- Edge node upgrades. Foghorn drives edge updates in place, and edge nodes report versions through `foghorn.node_components`.
- Downgrades across contract migrations. Rollback never undoes schema.

## Proposal

### Shared building block (exists)

`min_source_version` plus host detection is the mechanism both proposals use:

- The loader validates each floor. It must name a declared release below its own and must raise the floor already in effect (`validateSourceFloors`, `cli/internal/releases/loader.go`).
- The floor carries forward (`MinSourceVersionFor`).
- It is enforced cluster-wide before mutation by `cluster upgrade` and `release apply`.
- It is published in release metadata and bound to the embedded catalog.

### Proposal A: one release at a time

The contract: a cluster may move only from release N to release N+1, where N+1 is the next declared catalog release.

- Enforcement options, cheapest first:
  1. Move the prior-release completeness check (`firstIncompletePriorRelease` over every ledger, plus the prior data-migration blockers) into `release apply` preflight, before host convergence and expand migrations. The refusal names the first incomplete release. Ungated releases stay skippable in practice, because they leave nothing in the ledger to miss. About 1 day.
  2. Also make the loader require every release to declare `min_source_version` equal to its predecessor. Every skip is then refused, gated or not. This adds about half a day, but an operator N releases behind runs N full rolling applies even when most of them change nothing on their cluster.
- Old images must stay published for as long as a cluster may be one release behind. GHCR and Docker Hub keep release tags today, and no retention job deletes them.
- Operator promise text: "Upgrade one release at a time with `frameworks cluster release apply`. The CLI refuses to skip a release that has unfinished migrations, and tells you which release to apply next."

### Proposal B: six-month upgrade window with a walker

The contract: any release tagged within the six months before the target is a valid source, and the CLI walks the releases in between.

- Each catalog release gains `supported_until`, derived from its tag date plus six months (or declared explicitly). The loader rejects a new floor that would strand a source still inside its window. Raising `schema_migration_floor` becomes a window-aware operation.
- Schema verification replays from every in-window base, not only from the latest tag. The set can be deduplicated to releases that carry migrations, which is still most of them in the v0.3 line.
- The walker re-derives the source from detection and the ledger at every step. It then runs expand, upgrade, transitions, data migrations and postdeploy for each intermediate gated release, using that release's images and that release's embedded knowledge. Ungated releases can be collapsed, but the catalog has no ungated flag today.
- Old images, transition handlers and data-migration handlers stay available for the whole window. Transition handlers are compiled into the CLI, so the CLI must keep handlers for every in-window release.
- A window cannot cross a lifecycle boundary such as v0.3.0.
- Cost: about 1 to 2 weeks for the window, loader rules and multi-base schema replay, plus 6 to 9 days for the walker, plus the recurring CI time of multi-base replay.
- Operator promise text: "Any release from the last six months upgrades directly to the current release. `frameworks cluster release apply` applies the releases in between for you."

### Recommendation

Adopt Proposal A, enforcement option 1. Its enforcement is built (see Current State): it closed the one real gap (refusal after partial mutation), reuses code that existed, and makes the documented contract match what the gates already enforce. Keep `min_source_version` for releases whose components assume something older releases lack, such as the `/ready` switch, instead of declaring it on every release.

Revisit Proposal B when one of the decision criteria below changes.

### Decision criteria

- Operator demand: a self-hosting operator who cannot upgrade weekly and asks for a published window.
- Cadence: bursts of 20 to 50 tags a month make a walk long (each gated step is a full rolling restart with 150-second readiness waits per service).
- Gated-release frequency: most v0.3 releases carry migrations, so collapsing ungated releases saves little.
- v1.0 timing: a stable API promise is a natural point to introduce a window.
- Paid maintenance: a window is a support promise with a cost, and may belong to a paid tier.
- CI and image-retention budget for multi-base replay and six months of images.

## Impact / Dependencies

- CLI only for Proposal A. Proposal B also touches the release workflow (tag dates into the catalog) and CI (multi-base schema replay).
- Operator documentation: `website_docs/src/content/docs/operators/release-lifecycle.mdx` ("Supported upgrade sources") states the contract once it is chosen.

## Alternatives Considered

- **Support statement only.** Publish the contract without enforcement. Half a day, but operators still learn about refusals mid-release.
- **`rollback_disabled` instead of floors.** Disabling rollback for every service in a readiness-changing release avoids the floor, but it removes automatic rollback platform-wide for that release.
- **True LTS with backports.** Out of scope for the reasons in Non-Goals.

## Risks & Mitigations

- **Stranded clusters (A).** A cluster far behind needs many applies. Mitigation: keep ungated releases skippable (option 1), and make the refusal name the next release.
- **Partly walked cluster (B).** A walk that fails midway leaves the cluster on an intermediate release. Mitigation: the walker re-derives the source at each step and resumes; every intermediate release is itself a supported state.
- **Detection gaps.** A replica reporting a digest or `dev` build blocks the floor gate by design. Mitigation: redeploy that replica from a release; the refusal names it.
- **CLI knowledge lag (B).** A CLI must carry transition handlers for every in-window release. Mitigation: handlers stay compiled until the window closes, and the loader rejects removing one early.

## Migration / Rollout

- The source-floor gate ships in v0.3.11. It is inert because no release declares a floor.
- The release after v0.3.11 is the first user. When it becomes the pending catalog release, it declares `min_source_version: v0.3.11`, and `ReadySince` is removed from `pkg/servicedefs`. `TestReadySinceRemovedOnceSourceFloorReachesIt` enforces that pairing. Quartermaster, Navigator, the doctor, the rendered registry and self-registration then move to `/ready`.
- Proposal A option 1 enforcement ships in v0.3.11. It changes only when a refusal happens, not what is refused, so it needs no catalog field or floor.

## Open Questions

- Which contract does FrameWorks promise, A or B, and from which release?
- Under A, is option 1 (gated releases only) the contract, or should every skip be refused (option 2)?
- Under B, is six months measured from the tag date of the source or of the target's predecessor, and does an rc tag count?
- Should a release carry an explicit "gated" flag, so a walker or a floor rule does not have to infer it from migrations, transitions and data migrations?
- Should the source floor also include edge nodes through `foghorn.node_components`, once an edge consumer depends on a release capability?
- Is refusing every rollback below a running release's floor right, or should the floor be one-directional for releases whose floor exists only for readiness consumers?

## References, Sources & Evidence

- [Source] `cli/internal/releases/loader.go`, `cli/internal/releases/catalog.yaml`: catalog fields, `MinSourceVersionFor`, `validateSourceFloors`.
- [Source] `cli/cmd/cluster_source_floor.go`: source-floor gate and host detection read.
- [Source] `cli/cmd/cluster_upgrade_gate.go`: `runUpgradePreDeployGate`, `firstIncompletePriorRelease`, `formatMigrationRemediation`.
- [Source] `cli/cmd/cluster_prior_release_preflight.go`: `enforcePriorReleasesComplete`, the `release apply` prior-release preflight; `cli/cmd/cluster_prior_release_preflight_test.go` proves the refusal precedes the first mutation, with and without `--dry-run`.
- [Source] `cli/cmd/cluster_release.go`: `release apply` order (host convergence, expand, upgrades, postdeploy), `checkFetchedSourceFloor`.
- [Source] `cli/pkg/provisioner/migration_floor_guard.go`: lifecycle-boundary refusal.
- [Source] `pkg/servicedefs/servicedefs.go`: `ReadyPath`, `ReadySince`, `ReadinessPath`, `ReadinessPathFor`.
- [Reference] `docs/architecture/cluster-rollout.md`, "Readiness contract".
- [Reference] `PLAN_PLATFORM_LESSONS.md`, P3, P4 and P12, and "`/ready` switch (P3) and upgrade contract RFC (P4, P12)".
- [Evidence] `git for-each-ref refs/tags` on 2026-09-19: 122 tags, monthly counts as listed under Current State.

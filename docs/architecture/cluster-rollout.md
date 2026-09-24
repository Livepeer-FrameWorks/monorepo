# Cluster Rollout - CLI Orchestration of Release, Provision, Apply, Upgrade, and OS Updates

How the operator CLI turns a cluster manifest into ordered, health-gated change on
hosts. `cluster release apply` (`cli/cmd/cluster_release.go`) is the primary
operator rollout path: one ordered, resumable plan that sequences migrations,
cross-service reconciliations, and upgrades behind fail-closed release-metadata
validation — see below. It composes the lower-level commands that share the
machinery in `cli/pkg/orchestrator/`: `cluster provision` (full desired state),
`cluster diff`/`cluster apply` (typed rolling updates), and `cluster upgrade`
(release-manifest version moves). `cluster os update` (OS package maintenance)
does not use the orchestrator: it renders an Ansible inventory and runs the
`cluster_os_update.yml` playbook directly. Edge components have a separate
Foghorn-driven in-place update path documented in `edge-deployment.md`.

## `cluster release plan/apply`: the release lifecycle

`cluster release plan` is the static preview. It resolves the manifest topology,
target release metadata, migration phases, platform artifacts, declared
transitions, managed dependencies, host infrastructure, and control-plane
desired state without decrypting secrets, opening SSH sessions, or dialing a
service. It is safe to use early in release preparation and CI.

`cluster release apply --dry-run` is intentionally different: it opens the same
access paths and performs the same live gates and transition `Check`s as an apply,
but makes no changes. This catches missing credentials and inconsistent live
state before the rollout. A static plan succeeding does not imply that the live
preflight will succeed.

`cluster release apply` runs a single ordered, resumable plan for a whole release
rather than leaving the operator to drive migrations, reconciliations, and version
moves by hand. Release-metadata validation is fail-closed: a malformed or
unresolved release manifest aborts the plan before any host is touched. Rollout
gating distinguishes **readiness** from **liveness**; see
[Readiness contract](#readiness-contract) for which path each gate probes and
for how long. Only a service with no usable HTTP readiness path (non-HTTP, or no
port) falls back to gating on `systemd` unit activity. This is gate selection
only — there is no host cordon, drain, or re-admission mechanism. Because the plan
is resumable, an interrupted run picks up at the first incomplete step instead of
restarting the whole release. Operator-facing usage (dry-run plan preview, gates,
and the `--yes` execution flow) is documented in
[operators/running-upgrades.mdx](../../website_docs/src/content/docs/operators/running-upgrades.mdx).

The release lifecycle classifies deployable things by owner instead of treating
every manifest entry as a platform binary:

| Class                       | Release behavior                                           | Operator lifecycle                                                                |
| --------------------------- | ---------------------------------------------------------- | --------------------------------------------------------------------------------- |
| Platform artifacts          | Upgraded in dependency order by `cluster release apply`    | Release manifest and release catalog                                              |
| Managed dependencies        | Reported, but never upgraded implicitly                    | Independent manifest pins and `cluster provision` or a component-specific command |
| Host infrastructure         | Reported, but never upgraded implicitly                    | Dedicated OS, database, or data lifecycle                                         |
| Control-plane desired state | Reported, but never reconciled implicitly during a release | `cluster control-plane plan/reconcile`                                            |

Before the first mutation, and under `--dry-run` too, `release apply` runs its
preflight in this order: fetched release metadata against the CLI (`min_cli_version`,
required transitions, `min_source_version`), artifact resolution, the source floor
against detected replica versions (`cli/cmd/cluster_source_floor.go`), then prior-release
completeness (`enforcePriorReleasesComplete`, `cli/cmd/cluster_prior_release_preflight.go`).
The last check reads every PostgreSQL/YugabyteDB service ledger that already has a
baseline or ledger, and the ClickHouse ledgers, through `firstIncompletePriorRelease`,
plus the required data migrations of earlier releases (`datamigrate.PreDeployBlockers`).
A gap refuses the release and names the lowest unfinished release to apply first.
Databases the release creates are skipped, because they are born from the current
baseline. Env contracts and required operator inputs are checked next. The per-service
pre-deploy gate (`runUpgradePreDeployGate`) repeats the ledger and data-migration checks
for every service it deploys, so `cluster upgrade` keeps the same refusal.

The mutating release sequence is host convergence, service database creation and
expand migrations, platform-artifact upgrades
with declared transitions at their ordering points, then postdeploy migrations.

Host convergence and every service upgrade roll hosts in criticality waves
(`cli/cmd/release_rollout_waves.go`). A host is CONTROL when it runs
Quartermaster, Navigator, Commodore, Purser, Bridge, or a data store (Postgres or
YugabyteDB, Kafka brokers or controllers, ClickHouse); MEDIA when it runs
Foghorn, Chandler, the Livepeer gateway or signer, Helmsman or Mist, Redis, or is
an edge; otherwise OTHER. It takes the most critical tier of anything it runs,
and an unclassified service is CONTROL. Privateer convergence runs a CONTROL
canary alone, the other CONTROL hosts one at a time, MEDIA hosts one per cell
(`hosts.<name>.cluster`) with cells in parallel, then OTHER hosts eight at a
time, and gates each wave on mesh health for its hosts. A service upgrade keeps
the dependency order across services and rolls one service's replicas by the
deploy's tier: CONTROL one at a time with the first as canary, MEDIA one per
cell with cells in parallel, OTHER together. The effective `MaxUnavailable`
(`DefaultStrategyFor` plus the manifest `update_strategy` override) caps the
parallel lanes, and every built-in default is 1. Yugabyte keeps its master
roll. A failure starts no further host, leaves every later wave untouched, and
names the untouched hosts; parallel hosts write line-prefixed output, with
Ansible output routed through `ansiblerun.WithOutputWriter`.
Contract migrations are universally deferred. `release apply` prints the exact
`cluster migrate --phase contract` command, which the operator runs only after
the release's rollback or observation window closes.

## Readiness contract

Every Go service serves two HTTP endpoints on its main port:

- `/health` is **liveness**: the process is up. Container `HEALTHCHECK`s, the
  dev compose stack, and ingress probes use it.
- `/ready` is **readiness**: the instance can serve. It is 503 until every
  background gRPC listener serves (a gRPC listener waits up to two minutes for
  its TLS files), while any of the service's own dependency checks fails
  (database, ClickHouse, Kafka producer, Chandler's object store, Helmsman's
  MistServer), and for the whole drain window after shutdown starts. It never
  depends on another FrameWorks service, so there is no startup cycle.

Go services serve `/ready` from `pkg/server.NewServiceRouter` from v0.3.11 on.
Chandler's store-backed `/ready` exists in every supported release. Releases
v0.3.10 and earlier answer `/ready` with 404 for every other service.
`pkg/servicedefs` records both facts: `ReadyPath` is the endpoint and
`ReadySince` is the first release that serves it (empty when every supported
release does). Consumers pick the path in one of two ways:

| Consumer                                                                                                           | Knows the probed binary's release?  | Path                                                                                       |
| ------------------------------------------------------------------------------------------------------------------ | ----------------------------------- | ------------------------------------------------------------------------------------------ |
| Native gate (`go_service` validate) and Compose gate (`compose_stack` validate), for deploy and automatic rollback | Yes, the version being deployed     | `ReadinessPathFor(version)`: `/ready` from `ReadySince` on, `/health` for an older release |
| Orchestrator gate in `cluster apply` (`GateForService`)                                                            | No, it restarts what is on the host | `ReadinessPath()`                                                                          |
| `cluster doctor` probes                                                                                            | No                                  | `ReadinessPath()`                                                                          |
| Quartermaster registry (rendered by the CLI and self-registration) and the health poller's fallback                | No                                  | `ReadinessPath()`                                                                          |
| Navigator load-balancer monitors (one monitor per pool, pools mix releases during a rolling upgrade)               | No                                  | `ReadinessPath()`; edge pool types and the root ingress pool keep `/health`                |

`ReadinessPath()` is `ReadyPath` only when `ReadySince` is empty, and
`HealthPath` otherwise, so a version-blind consumer never probes `/ready` on a
binary that may predate it. In v0.3.11 this means the rollout gates probe
`/ready` for v0.3.11 binaries and `/health` for a rollback to v0.3.10, while the
version-blind consumers stay on `/health` for every Go service except Chandler.

The version-blind consumers move to `/ready` when the release catalog guarantees
that no older binary can be running: a release declares `min_source_version` at
or above `ReadySince`, and `ReadySince` is removed from `servicedefs` in the same
change. `TestReadySinceRemovedOnceSourceFloorReachesIt`
(`cli/internal/releases/source_floor_test.go`) fails until both happen together.

**Source floor.** `min_source_version` on a catalog release is the lowest
release a cluster may run when it moves to that release or back from it; the
effective floor carries forward from earlier releases
(`releases.MinSourceVersionFor`). `cluster upgrade` and `cluster release apply`
detect the release every platform-artifact replica runs (host detection, the
same `cli/pkg/detect` read the upgrade uses per host) and refuse, before any
mutation, when a replica is below the target's floor, when the target is below a
running replica's floor (a rollback past the floor), or when a replica's version
cannot be read as a release (`cli/cmd/cluster_source_floor.go`). There is no
override flag. When no catalog release declares a floor, nothing is read. The
release metadata carries the effective floor, and the CLI refuses a fetched
manifest whose floor disagrees with its own catalog. Edge nodes are not part of
the floor: no consumer probes Helmsman's `/ready`, and Navigator's edge pools
use the edge ingress `/health`.

**Waits.** The native and Compose validate steps, the `cluster upgrade` health
wait (including the automatic rollback's), and the orchestrator's default gate
all wait up to 150 seconds (`provisioner.RolloutReadinessTimeout`, and the
`go_service_validate_timeout` and `compose_stack_validate_timeout` role
defaults). The Livepeer gateway keeps its own 300-second wait.

## Control-plane desired state

Provisioning establishes both hosts and platform-owned service state on a fresh
cluster. Subsequent changes to tenant bootstrap, the Quartermaster service
catalog, billing tiers and prices, managed accounts, or service-cluster
assignments do not require reprovisioning hosts. They use the explicit,
idempotent control-plane lifecycle:

```text
frameworks cluster control-plane plan --manifest <path> --domain <domain>
frameworks cluster control-plane reconcile --manifest <path> --domain <domain>
```

Domains are `quartermaster`, `billing`, `accounts`, `assignments`, `validation`,
or `all`. The plan resolves and validates the rendered desired state without
opening remote sessions or changing services. Reconcile changes service-owned
control-plane state, but never provisions hosts, deploys binaries, or runs schema
migrations. The deprecated `cluster finalize` command remains a compatibility
alias, not a primary operator workflow.

## Planner and dependency graph

The planner (`orchestrator/planner.go`) expands the manifest into `Task`s across
four phases, executed in order:

```
mesh  ─►  infrastructure  ─►  applications  ─►  interfaces
(Privateer/WG up      (Postgres, Kafka,      (FrameWorks         (Caddy, chartroom,
 before anything)      ClickHouse, Redis)     services)           foredeck)
```

Tasks carry explicit `DependsOn` edges; `DependencyGraph.TopologicalSort` emits
parallel-executable batches where tasks in a batch share **no host and no unresolved
dependency** — so a batch can run fully parallel without same-host contention. This
`ExecutionPlan`/batches model drives `cluster provision`.

## Typed diff → rolling waves (`cluster diff` / `cluster apply`)

`cluster apply` never re-runs full provisioning. It composes:

1. **Diff classification** (`orchestrator/diff.go`): per host/service, desired file
   fingerprints (binary, env, unit, cert) are compared against observed sha256s and
   classified into `DiffKind`s. Services without a registered fingerprinter, or any
   unmodeled change, classify as `unknown`. `cluster apply` **refuses** topologies
   containing `unknown` or `infra` diffs and points at `cluster provision` — it
   only handles changes it can fully model.
2. **Wave building** (`orchestrator/rollout.go`): per service, `BuildWaves` is a
   pure function partitioning hosts into sequential waves by `UpdateStrategy`:
   - `MaxUnavailable` — hosts rolled per wave; zero-value means one at a time
     (missing strategy can never roll a whole tier).
   - `Canary` — first wave soaks at most N hosts before normal cadence resumes.
   - `RegionStagger` — regions become contiguous blocks; no wave mixes regions
     (paired regional tiers keep one region fully healthy).
   - `PrimaryLast` — `primary`-role tasks roll after all others, forced to
     `MaxUnavailable=1` (Redis: replicas → sentinel failover → old primary).

   Defaults are baked per service in `strategy_defaults.go`: quorum tiers
   (yugabyte, kafka-controller) and brokers at 1; stateless paired-regional tiers
   (foghorn, bridge, chandler, signalman, decklog, periscope-ingest,
   livepeer-gateway) get `canary=1 + region_stagger`; unknown services fall through
   to the maximally cautious default.

3. **Health gates** (`orchestrator/gate.go`): between waves the executor waits for
   every host to pass its gate — `HTTPReady` (SSH + curl against the service's
   localhost health endpoint, auto-selected from `pkg/servicedefs`) or
   `SystemdActive` for services without one. SSH errors and non-2xx are treated
   identically ("not ready"), so a probe failure can't bypass the gate.
4. **Execution** (`orchestrator/executor.go`): env-only diffs on reload-capable
   services (`SupportsSIGHUPReload`) get `reload`; anything touching
   binary/unit/cert gets `restart`. Any host failing its action or gate **halts**
   the rollout: later waves never start. There is no automatic rollback in
   `cluster apply` — re-running against the same manifest is the recovery; the diff
   classifier converges from wherever the halt left things.

Without `--confirm`, `cluster apply` is read-only and prints the exact waves.

## Content-addressed pinning

Versions resolve through GitOps release manifests (`cli/pkg/gitops/`):

- Binaries are pinned by URL + `checksum` (`sha256:`/`sha512:` hex) per arch;
  the provisioner passes the checksum to the host-side download for verification.
- Docker infrastructure components pin an OCI manifest **digest**, not a floating
  tag.
- Channels (`stable`, `candidate`, and legacy `rc`) resolve to concrete versions at plan time; pinned
  versions are cached with a TTL/max-staleness policy so repeated runs don't
  re-fetch. A channel names a resolution rule, never a version.

## `cluster upgrade` and auto-rollback

`cluster upgrade [service] | --all` moves a service between release-manifest
versions: detect current version/mode → fetch target → stop → pull/download →
start → validate health. On health-check failure it automatically rolls back to
the previous version unless `--no-rollback` (or when the recorded previous
version/mode is incomplete, in which case rollback is disabled with a warning).
The rollback's health check probes the readiness path of the restored release,
so restoring a binary that predates `/ready` is gated on `/health`.

Rollback restores the service artifact only: it does **not** undo schema or data
migrations. `cluster upgrade` is a low-level recovery primitive; the normal whole
release path is `cluster release apply`. `--all` upgrades enabled services in
dependency order, but it neither executes nor proves declared release
transitions. It therefore fails closed rather than crossing a transition's
downstream service boundary unsafely. Contract migrations remain a separate,
deferred operator action in both flows.

## OS updates (`cluster os update`)

Production nodes never install OS updates in the background (the tuning role
disables unattended-upgrades). `cluster os update --check` inventories pending
updates; `--apply` runs the mutating playbook host by host.

- Concurrency is controlled **only** by `--serial N` (default 1: how many hosts the
  playbook processes in parallel) plus `--no-reboot` / `--continue-on-error`.
- **There is no central coordination of host selection.** The playbook takes a
  per-host advisory lock (`mkdir /var/lib/frameworks/locks/os_update.lock`), so two
  `--apply` runs hitting the same host fail closed (exit 75, printing the lock
  owner) — but nothing coordinates which hosts different operators target, and
  there is no integration with Foghorn node drain — the operator is the
  serialization point across hosts. Drain-aware OS updates are a proposal
  (`docs/rfcs/cluster-os-update-drain.md`), not built.

## Key Files

- `cli/pkg/orchestrator/types.go` - phases, Task identity, `UpdateStrategy`
- `cli/pkg/orchestrator/planner.go` - manifest → tasks per phase
- `cli/pkg/orchestrator/dependency.go` - batched topological sort
- `cli/pkg/orchestrator/diff.go` - typed diff classification
- `cli/pkg/orchestrator/rollout.go` - `BuildWaves`
- `cli/pkg/orchestrator/gate.go` - `HTTPReady` / `SystemdActive` gates
- `cli/pkg/orchestrator/executor.go` - wave execution, halt semantics
- `cli/pkg/orchestrator/strategy_defaults.go` - per-service strategy registry
- `cli/cmd/cluster_release.go` - static planning and ordered release apply
- `cli/cmd/cluster_control_plane.go` - explicit desired-state plan/reconcile
- `cli/cmd/cluster_apply.go`, `cli/cmd/cluster_upgrade.go`, `cli/cmd/cluster_os_update.go`
- `cli/cmd/cluster_source_floor.go` - `min_source_version` enforcement against detected replica versions
- `cli/cmd/cluster_prior_release_preflight.go` - prior-release completeness in `release apply` preflight
- `pkg/servicedefs/servicedefs.go` - `ReadyPath`, `ReadySince`, `ReadinessPath()`, `ReadinessPathFor()`
- `cli/pkg/gitops/` - release manifests, checksums, digests, channel resolution

## Gotchas

- A service missing from the strategy registry silently gets the slow-but-safe
  path (one host per wave). That is intentional; add a registry entry rather than
  overriding per-run.
- `cluster apply --confirm` halting mid-service is a normal, recoverable state —
  the fleet is heterogeneous until the next successful run, and `cluster diff`
  shows exactly which hosts still differ.
- Auto-rollback exists only in `cluster upgrade` (health-gated version restore).
  Neither `cluster apply` nor `cluster provision` roll anything back.

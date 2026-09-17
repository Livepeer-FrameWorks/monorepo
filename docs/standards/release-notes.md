# Release Notes Format

How we write release notes for FrameWorks. Established with v0.2.32.

## Principles

- **Audience-first upgrade section.** What does each persona have to _do_ to land this release? That goes at the top, before the feature list. Most readers care about that before they care about what changed.
- **Read the actual diffs, not commit subjects.** Commit messages routinely mislabel "exposed in webapp" or "hardened" as if they were new features. Verify by looking at migrations, new files, new proto messages, and earliest appearance via `git log -S`.
- **New vs Hardened vs Fixed are separate categories.** If something already existed and this release polished it, that's `Hardened`, not `New`. Be honest. "Exposed better in the API" is not a feature, it's an improvement to an existing one.
- **Flag pre-upgrade gotchas in the Upgrade section, not in Fixes.** Fail-closed migrations, NOT VALID/VALIDATE CHECK constraints on existing rows, mandatory re-declarations, etc. all go above the command block so operators see them before they run.
- **Name every manual operator input.** If a release adds or changes a GitOps value, SOPS secret, manifest field, provider credential, or generated key, say exactly what the operator must set, where it belongs, how to create or update it, and how to verify it before deployment. If there are no manual input changes, say so.
- **Keep release notes operationally complete but short.** Release notes provide the release-specific inputs, deviations, and command sequence. Detailed SQL, repair procedures, and per-environment transcripts belong in the linked operator runbook.
- **One canonical body.** The published GitHub Release body is the release note. Do not maintain a second release-specific copy in the repository. This file defines the authoring process, not individual releases.
- **No em dashes.** Use commas, parens, or periods.
- **Backtick literal identifiers** (`dvr+{chapter_id}`, `lost_local`, env var names, etc.) so they render as code instead of being mangled by markdown.

## Structure

The GitHub Release body uses these sections in order:

1. `# Release vX.Y.Z`
2. Optional preamble for first-of-a-format or unusual releases.
3. `## Upgrade` with persona subsections (see below).
4. `## New`, `## Hardened`, `## Fixes`, `## Build / infra`, `## Docs`. Drop any heading that has nothing under it. No empty sections.

### Upgrade subsections

**`### Cluster operators (anyone running their own FrameWorks cluster)`**

Open with the supported source version and minimum CLI version when either is constrained. Describe schema work only when it changes the operator procedure. Do not publish migration counts or a per-service migration inventory just because migrations exist.

#### Operator-managed inputs

Before the CLI block, list every release-specific manual input. For each input, state:

- the exact env key, manifest field, DNS/provider setting, or generated-key family;
- whether it must be added, refreshed, rotated, preserved, removed, or merely verified;
- the canonical GitOps location, distinguishing plaintext configuration from SOPS-encrypted secrets;
- the exact repository helper or CLI generator to use when one exists;
- a safe verification command or expected state that does not reveal secret values;
- rotation and compatibility consequences when replacing an existing value.

Only list inputs changed by this release. Do not dump the complete environment contract into every release. Never include real secret values, tell operators to edit encrypted files directly, or use vague instructions such as "update the env vars." For `gitops/secrets/*.env`, use the repository's supported SOPS helper. For generated secret families, name the narrow generator intended for upgrades so operators do not rotate unrelated credentials.

If the release has no manual GitOps, SOPS, manifest, DNS, or provider changes, write: `No GitOps or secret changes are required for this release.`

#### Normal CLI lifecycle

Use one manifest source consistently throughout the examples. Public notes normally use `--manifest <path>`. An operator-specific note may instead use `--gitops-dir <dir> --cluster <name>` plus `--age-key <path>` when the source contains SOPS-encrypted files.

The compact normal sequence is:

    frameworks cluster release plan --manifest <path> --version vX.Y.Z
    frameworks cluster release apply --manifest <path> --version vX.Y.Z --dry-run
    frameworks cluster release apply --manifest <path> --version vX.Y.Z --yes
    frameworks cluster status --manifest <path>
    frameworks cluster doctor --manifest <path> --deep
    frameworks cluster diff --manifest <path>

`release plan` is the credential-free, static preview. `release apply --dry-run` is the live preflight: it resolves access and authentication, then runs the same migration, transition, and service checks the real rollout will use. `release apply` owns the ordered expand migrations, service upgrades, declared release transitions, and postdeploy migrations. Do not duplicate those steps in the normal-path command block.

Do not put `cluster migrate validate` in routine operator instructions. It validates the migration files embedded in the installed CLI and takes no manifest because it does not inspect a cluster. Release CI and `make release-preflight` own that source/package validation. It remains a maintainer and diagnostic command, not a deployment phase.

`cluster diff` is verification, not a mandatory invitation to run `cluster provision`. If it reports intended infrastructure or rendered-config drift, name the specific reconciliation command separately.

#### Data migrations and contract migrations

Service-owned data migrations are separate from schema migrations. `release apply` owns expand and postdeploy schema phases, but it does not execute `cluster data-migrate`; catalogued data migrations are explicit operator steps because they may scan or rewrite live service data in resumable batches.

When the target release declares a data migration, include its exact dry-run, run, and verify commands after the target service binaries are live:

    frameworks cluster data-migrate run <service>.<migration_id> --manifest <path> --dry-run
    frameworks cluster data-migrate run <service>.<migration_id> --manifest <path>
    frameworks cluster data-migrate verify <service>.<migration_id> --manifest <path>

State the catalog gate. A migration required before `postdeploy` may intentionally make the first `release apply` stop at that gate; after `run` and `verify`, rerun the same `release apply --yes` command. A migration required only before `contract` runs after the normal release application and does not require a second `release apply`. Include it in the introducing release so operators complete the data conversion before a later destructive phase makes it urgent.

Contract migrations are always outside `release apply`. When a release has contract migrations, name the rollback or observation window and append a separate, explicitly deferred block:

    frameworks cluster migrate --manifest <path> --phase contract --to-version vX.Y.Z --dry-run
    frameworks cluster migrate --manifest <path> --phase contract --to-version vX.Y.Z --yes

Classify everything else explicitly instead of using `cluster provision` as a catch-all:

- **Platform artifact:** handled by `cluster release apply`.
- **Managed dependency or rendered host configuration:** name the exact component-specific reconciliation command and when to run it. If only `cluster provision` can perform the work, use the narrowest supported selector, explain exactly what it changes and why `release apply` does not own it, and list it once after the release application. Never add an unscoped `cluster provision` as a second generic deployment pass.
- **Control-plane desired state:** use `cluster control-plane plan`, then `cluster control-plane reconcile` with the affected domain(s).
- **Host or data infrastructure:** name the dedicated lifecycle command and its own safety procedure.

The manual `cluster migrate` plus `cluster upgrade` sequence is a low-level recovery path, not the documented normal path. Include it only when a release has a diagnosed recovery need, and spell out every migration phase and transition ordering point. Direct `cluster upgrade --all` does not execute declared release transitions and may refuse an unsafe downstream upgrade.

End with a one-line rollback expectation. Expand and postdeploy migrations must remain rollback-compatible for the supported window; running contract migrations closes that window and requires an explicit forward fix.

**`### Tenants on a managed FrameWorks cluster`**

Usually "Nothing to do" plus a one-liner on which new features show up automatically. If a behavior change is visible (e.g. plan tier admission tightening), name it here too.

**`### Edge-only self-hosters`**

Usually "Nothing to do" because Foghorn's edge release reconciler pushes new Helmsman/Caddy versions over the existing Helmsman stream. Mention this mechanism explicitly so readers understand why.

When a release raises the minimum edge protocol, changes persistent edge state, or cannot update old edges in-band, replace "Nothing to do" with the exact control-plane-first or edge-first ordering, the concrete edge command, expected interruption, state-volume requirements, and recovery restriction.

**`### Tenant-private / marketplace cluster operators`**

Use this subsection when the release affects tenant-owned, marketplace, or otherwise non-platform-official clusters. These operators follow the cluster-operator upgrade path, but call out the specific manifest fields, DNS/TLS prerequisites, and provisioning steps they must perform. Do not collapse this into edge-only self-hosting; running a cluster and running a BYO edge node are different responsibilities.

### Body sections

- **New.** Net-new functionality only. Each bullet: name, then one to two sentences on what it actually is. Backtick literal identifiers.
- **Hardened.** Improvements to things that already existed. Edge cases fixed, robustness work, better surfaces, perf passes.
- **Fixes.** Bug fixes. Brief.
- **Build / infra.** Build flags, dependency changes, migration runner changes, CI changes that operators or builders might notice. Once the release-plan tool is wired into CI (see `docs/architecture/build-and-packaging.md`), this section also reports the per-release **carry-forward summary**: how many components rebuilt, how many carried, and (if non-trivial) which ones. Operators read this to know whether `cluster upgrade` will re-pull anything for them.
- **Docs.** New or significantly rewritten documentation.

### Carried components

When release-plan emits `carry_forward` decisions, surface them honestly:

- Use the **artefact provenance** language: "helmsman carries forward from v0.2.37" — never "helmsman is at v0.2.37 in this release" without context, since that reads as a downgrade.
- If an edge component (`helmsman`, `mist`, `caddy`) carries forward, mention it under the `### Edge-only self-hosters` upgrade subsection too — Foghorn will not roll those nodes, and that is the desired outcome operators should see explicitly.
- Don't mix "carried" with "unchanged behavior." A bumped third-party dep that doesn't change the binary's source-hash still rebuilds (because go.sum changed); a no-op release matters only when the operator-visible identity actually didn't move.

## Authoring checklist

Run these before writing a single bullet.

Before creating a platform tag, commit the new release entry and all schema or
migration changes, then use the guarded tag path:

    make release-preflight RELEASE_VERSION=vX.Y.Z
    make release-tag RELEASE_VERSION=vX.Y.Z
    git push origin vX.Y.Z

`release-preflight` requires the target to be the latest entry in
`cli/internal/releases/catalog.yaml`, verifies that the release-controlled files
are committed, validates tag/catalog/migration state, and asks the CLI to emit
the target's compatibility metadata. `release-tag` repeats that preflight before
creating an annotated local tag; it never pushes. The tag-triggered release
workflow runs the same preflight before release planning or artifact builds.

0.  **Read the previous release.** Pulls in the exact tone and structure so this release matches the last one. Either:

        gh release view <prev-tag>

    If there's no previous release, skip to step 1 but flag that this is the first formatted release in the preamble.

1.  **Get the commit list.**

    git log <prev-tag>..<this-tag> --oneline

2.  **Get the diff stat to find big commits.**

    git diff <prev-tag>..<this-tag> --stat | tail -50

3.  **Audit operator-managed inputs.** Read the configuration and deployment diff, then inspect the operator GitOps source used for the deployment (for the managed platform, `../gitops`). Trace every newly required or changed input back to its runtime validation and renderer:

        git diff --name-only <prev-tag>..<this-tag> -- \
          config .env.example docker-compose.yml cli/pkg/inventory \
          cli/pkg/provisioner docs/standards/environment-configuration.md \
          website_docs/src/content/docs/operators

    Classify each delta as derived, defaulted, plaintext GitOps, SOPS secret, manifest topology, or external-provider state. Release notes include only operator-owned changes. Confirm the exact helper command and a non-secret verification path; do not infer either from an example comment.

4.  **List every migration in this release.**

        git diff --name-only <prev-tag>..<this-tag> -- pkg/database/sql/migrations | sort

    Then read each one. Migrations are the strongest signal of net-new functionality. New tables and new columns almost always mean a new feature. CHECK constraints over existing rows are pre-upgrade gotchas when existing data may violate them. Postdeploy and contract migrations need separate operator instructions.

5.  **Read every new file.** `git show --stat <commit>` and look for the lines without an existing path. New `.go` files in `internal/control/` or `internal/grpc/` usually mean a new subsystem. New top-level packages under `pkg/` are often shared primitives worth calling out.

6.  **Read proto additions.**

        git diff <prev-tag>..<this-tag> -- pkg/proto/*.proto | grep "^+" | grep -E "rpc |^\+message "

    New RPCs are user-facing surface changes.

7.  **Read GraphQL additions.**

    git diff <prev-tag>..<this-tag> -- pkg/graphql/schema.graphql | grep "^+"

8.  **Verify "new" claims with `git log -S`.** If you think a feature is new, confirm:

        git log --all --oneline -S "<symbol>" | tail

    If it predates the previous tag, it's not new in this release.

9.  **Trace the CLI path.** Confirm the release catalog, minimum CLI, required transitions, data-migration gates, rollback restrictions, edge protocol floor, and contract behavior from code and generated release metadata. Do not reconstruct deployment ordering from an older release note.

10. **Map every commit to a category.** If a commit doesn't fit `New / Hardened / Fixes / Build / Docs`, push back on whether it belongs in the release notes at all.

11. **Replace the generated GitHub body.** Write the reviewed Markdown to a temporary file and replace the release body instead of appending to GitHub's generated notes:

        gh release edit <tag> --notes-file <temporary-notes-file>

    Verify the published body with `gh release view <tag>`. It must contain one comparison link and no generated-note duplication.

## Anti-patterns

- **Listing commit subjects as features.** "Wire up ClickHouse migrations" is plumbing, not a feature. Skip or move to `Build / infra`.
- **Publishing migration arithmetic.** Counts by service or phase are not useful release prose. State only the migration stages and operator actions that affect this rollout.
- **Vague GitOps instructions.** "Update the environment" is not actionable. Name the changed keys or fields, their canonical file class, the supported edit/generation command, and a safe verification.
- **Dumping the runbook into the release.** Include the normal CLI path and release-specific deviations. Link lengthy SQL, repair branches, host-by-host transcripts, and incident recovery to canonical operator docs.
- **Calling things new when they're not.** "Signing keys, stream pulls and edge clusters surfaced better in API and webapp" is `Hardened`, because the underlying capability already shipped.
- **Mixing audience instructions.** Don't tell self-hosters to run `frameworks cluster migrate`. They run edge nodes; Foghorn reconciles them.
- **Inventing categories.** If there's nothing to put in `Build / infra`, drop the heading. No empty sections.
- **Burying pre-upgrade gotchas in Fixes.** If an operator must do something before binaries roll, it goes in the Upgrade section, not down in Fixes where they'll find it after the fact.

## Companion copy

Each release also gets a Discord post and an X / Twitter post. Both are strict subsets of the GitHub release `New` section, ordered by what a builder would care about most. Discord can take six to eight bullets with a tiny bit of color; X gets the same content trimmed to fit, no emojis required.

# Release Notes Format

How we write release notes for FrameWorks. Established with v0.2.32.

## Principles

- **Audience-first upgrade section.** What does each persona have to _do_ to land this release? That goes at the top, before the feature list. Most readers care about that before they care about what changed.
- **Read the actual diffs, not commit subjects.** Commit messages routinely mislabel "exposed in webapp" or "hardened" as if they were new features. Verify by looking at migrations, new files, new proto messages, and earliest appearance via `git log -S`.
- **New vs Hardened vs Fixed are separate categories.** If something already existed and this release polished it, that's `Hardened`, not `New`. Be honest. "Exposed better in the API" is not a feature, it's an improvement to an existing one.
- **Flag pre-upgrade gotchas in the Upgrade section, not in Fixes.** Fail-closed migrations, NOT VALID/VALIDATE CHECK constraints on existing rows, mandatory re-declarations, etc. all go above the command block so operators see them before they run.
- **No em dashes.** Use commas, parens, or periods.
- **Backtick literal identifiers** (`dvr+{chapter_id}`, `lost_local`, env var names, etc.) so they render as code instead of being mangled by markdown.

## Structure

The release file is `docs/releases/vX.Y.Z.md` (or wherever your release pipeline draws from). Sections in order:

1. `# Release vX.Y.Z`
2. Optional preamble for first-of-a-format or unusual releases.
3. `## Upgrade` with persona subsections (see below).
4. `## New`, `## Hardened`, `## Fixes`, `## Build / infra`, `## Docs`. Drop any heading that has nothing under it. No empty sections.

### Upgrade subsections

**`### Cluster operators (anyone running their own FrameWorks cluster)`**

Open with a one-line migrations summary (count, which databases, expand vs postdeploy vs contract). Then any pre-upgrade gotchas (fail-closed columns that need pre-declaration, plan-tier reclassifications, etc.). The normal command sequence is:

    frameworks cluster release plan --manifest <path> --version vX.Y.Z
    frameworks cluster release apply --manifest <path> --version vX.Y.Z --dry-run
    frameworks cluster release apply --manifest <path> --version vX.Y.Z --yes
    frameworks cluster status --manifest <path>

`release plan` is the credential-free, static preview. `release apply --dry-run` is the live preflight: it resolves access and authentication, then runs the same migration, transition, and service checks the real rollout will use. `release apply` owns the ordered expand migrations, service upgrades, declared release transitions, and postdeploy migrations. Do not duplicate those steps in the normal-path command block.

Contract migrations are always outside `release apply`. When a release has contract migrations, name the rollback or observation window and append a separate, explicitly deferred block:

    frameworks cluster migrate --manifest <path> --phase contract --to-version vX.Y.Z --dry-run
    frameworks cluster migrate --manifest <path> --phase contract --to-version vX.Y.Z

Classify everything else explicitly instead of using `cluster provision` as a catch-all:

- **Platform artifact:** handled by `cluster release apply`.
- **Managed dependency or rendered host configuration:** name the exact `cluster provision` or component-specific reconciliation command and when to run it.
- **Control-plane desired state:** use `cluster control-plane plan`, then `cluster control-plane reconcile` with the affected domain(s).
- **Host or data infrastructure:** name the dedicated lifecycle command and its own safety procedure.

The manual `cluster migrate` plus `cluster upgrade` sequence is a low-level recovery path, not the documented normal path. Include it only when a release has a diagnosed recovery need, and spell out every migration phase and transition ordering point. Direct `cluster upgrade --all` does not execute declared release transitions and may refuse an unsafe downstream upgrade.

End with a one-line rollback expectation. Expand and postdeploy migrations must remain rollback-compatible for the supported window; running contract migrations closes that window and requires an explicit forward fix.

**`### Tenants on a managed FrameWorks cluster`**

Usually "Nothing to do" plus a one-liner on which new features show up automatically. If a behavior change is visible (e.g. plan tier admission tightening), name it here too.

**`### Edge-only self-hosters`**

Usually "Nothing to do" because Foghorn's edge release reconciler pushes new Helmsman/Caddy versions over the existing Helmsman stream. Mention this mechanism explicitly so readers understand why.

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

0.  **Read the previous release.** Pulls in the exact tone and structure so this release matches the last one. Either:

        gh release view <prev-tag>

    or read the previous file under `docs/releases/`. If there's no previous release, skip to step 1 but flag that this is the first formatted release in the preamble.

1.  **Get the commit list.**

    git log <prev-tag>..<this-tag> --oneline

2.  **Get the diff stat to find big commits.**

    git diff <prev-tag>..<this-tag> --stat | tail -50

3.  **List every migration in this release.**

        git diff --name-only <prev-tag>..<this-tag> -- pkg/database/sql/migrations | sort

    Then read each one. Migrations are the strongest signal of net-new functionality. New tables and new columns almost always mean a new feature. CHECK constraints over existing rows are pre-upgrade gotchas when existing data may violate them. Postdeploy and contract migrations need separate operator instructions.

4.  **Read every new file.** `git show --stat <commit>` and look for the lines without an existing path. New `.go` files in `internal/control/` or `internal/grpc/` usually mean a new subsystem. New top-level packages under `pkg/` are often shared primitives worth calling out.

5.  **Read proto additions.**

        git diff <prev-tag>..<this-tag> -- pkg/proto/*.proto | grep "^+" | grep -E "rpc |^\+message "

    New RPCs are user-facing surface changes.

6.  **Read GraphQL additions.**

    git diff <prev-tag>..<this-tag> -- pkg/graphql/schema.graphql | grep "^+"

7.  **Verify "new" claims with `git log -S`.** If you think a feature is new, confirm:

        git log --all --oneline -S "<symbol>" | tail

    If it predates the previous tag, it's not new in this release.

8.  **Map every commit to a category.** If a commit doesn't fit `New / Hardened / Fixes / Build / Docs`, push back on whether it belongs in the release notes at all.

## Anti-patterns

- **Listing commit subjects as features.** "Wire up ClickHouse migrations" is plumbing, not a feature. Skip or move to `Build / infra`.
- **Calling things new when they're not.** "Signing keys, stream pulls and edge clusters surfaced better in API and webapp" is `Hardened`, because the underlying capability already shipped.
- **Mixing audience instructions.** Don't tell self-hosters to run `frameworks cluster migrate`. They run edge nodes; Foghorn reconciles them.
- **Inventing categories.** If there's nothing to put in `Build / infra`, drop the heading. No empty sections.
- **Burying pre-upgrade gotchas in Fixes.** If an operator must do something before binaries roll, it goes in the Upgrade section, not down in Fixes where they'll find it after the fact.

## Companion copy

Each release also gets a Discord post and an X / Twitter post. Both are strict subsets of the GitHub release `New` section, ordered by what a builder would care about most. Discord can take six to eight bullets with a tiny bit of color; X gets the same content trimmed to fit, no emojis required.

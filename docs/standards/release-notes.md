# Release Notes Format

How we write release notes for FrameWorks. Established with v0.2.32.

## Principles

- **Audience-first upgrade section.** What does each persona have to _do_ to land this release? That goes at the top, before the feature list. Most readers care about that before they care about what changed.
- **Read the actual diffs, not commit subjects.** Commit messages routinely mislabel "exposed in webapp" or "hardened" as if they were new features. Verify by looking at migrations, new files, new proto messages, and earliest appearance via `git log -S`.
- **New vs Hardened vs Fixed are separate categories.** If something already existed and this release polished it, that's `Hardened`, not `New`. Be honest. "Exposed better in the API" is not a feature, it's an improvement to an existing one.
- **No em dashes.** Use commas, parens, or periods.
- **Backtick literal identifiers** (`dvr+{chapter_id}`, `lost_local`, env var names, etc.) so they render as code instead of being mangled by markdown.

## Structure

```markdown
# Release vX.Y.Z

(Optional preamble for first-of-a-format or unusual releases.)

## Upgrade

### Cluster operators (anyone running their own FrameWorks cluster)

Migrations summary (how many, which databases, which phase). Example commands
operators might run, with `--to-version vX.Y.Z` set correctly (the version
you're going _to_, not from). Link to the docs for full instructions.
```

frameworks cluster migrate validate
frameworks cluster migrate --manifest <path> --phase expand --to-version vX.Y.Z --dry-run
frameworks cluster migrate --manifest <path> --phase expand --to-version vX.Y.Z
frameworks cluster upgrade plan --manifest <path> --version vX.Y.Z
frameworks cluster upgrade <service> --manifest <path> --version vX.Y.Z

```

One line on rollback expectation (expand-only = old binaries still work, etc.).

### Tenants on a managed FrameWorks cluster

Usually "Nothing to do" plus a one-liner on which new features show up automatically.

### Self-hosters

Usually "Nothing to do" because Foghorn's edge release reconciler pushes new
Helmsman/Caddy versions over the existing Helmsman stream. Mention this
mechanism explicitly so readers understand why.

## New

Net-new functionality only. Each bullet: name, then one to two sentences on
what it actually is. Backtick literal identifiers.

## Hardened

Improvements to things that already existed. Edge cases fixed, robustness
work, better surfaces, perf passes.

## Fixes

Bug fixes. Brief.

## Build / infra

Build flags, dependency changes, migration runner changes, CI changes that
operators or builders might notice.

## Docs

New or significantly rewritten documentation.
```

## Authoring checklist

Run these before writing a single bullet.

1. **Get the commit list.**

   ```
   git log <prev-tag>..<this-tag> --oneline
   ```

2. **Get the diff stat to find big commits.**

   ```
   git diff <prev-tag>..<this-tag> --stat | tail -50
   ```

3. **Read every migration in this release.**

   ```
   git show <commit> -- pkg/database/sql/migrations/**/<this-version>/
   ```

   Migrations are the strongest signal of net-new functionality. New tables and new columns almost always mean a new feature.

4. **Read every new file.** `git show --stat <commit>` and look for the lines without an existing path. New `.go` files in `internal/control/` or `internal/grpc/` usually mean a new subsystem.

5. **Read proto additions.**

   ```
   git diff <prev-tag>..<this-tag> -- pkg/proto/*.proto | grep "^+" | grep -E "rpc |^\+message "
   ```

   New RPCs are user-facing surface changes.

6. **Verify "new" claims with `git log -S`.** If you think a feature is new, confirm:

   ```
   git log --all --oneline -S "<symbol>" | tail
   ```

   If it predates the previous tag, it's not new in this release.

7. **Map every commit to a category.** If a commit doesn't fit `New / Hardened / Fixes / Build / Docs`, push back on whether it belongs in the release notes at all.

## Anti-patterns

- **Listing commit subjects as features.** "Wire up ClickHouse migrations" is plumbing, not a feature. Skip or move to `Build / infra`.
- **Calling things new when they're not.** "Signing keys, stream pulls and edge clusters surfaced better in API and webapp" is `Hardened`, because the underlying capability already shipped.
- **Mixing audience instructions.** Don't tell self-hosters to run `frameworks cluster migrate`. They run edge nodes; Foghorn reconciles them.
- **Inventing categories.** If there's nothing to put in `Build / infra`, drop the heading. No empty sections.

## Companion copy

Each release also gets a Discord post and an X / Twitter post. Both are
strict subsets of the GitHub release `New` section, ordered by what a builder
would care about most. Discord can take six to eight bullets with a tiny bit
of color; X gets the same content trimmed to fit, no emojis required.

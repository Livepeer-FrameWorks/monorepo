# Code Comments

This document defines what a code comment in FrameWorks should and should not do. It applies to all languages (Go, TS, Svelte, Rust, SQL, proto, GraphQL).

## Core Rule

A code comment must explain one of:

1. **Current behavior** that the code itself does not make obvious.
2. **A non-obvious invariant** (ordering, concurrency, security, tenant-scoping).
3. **Why** the code is written this way at this site (protocol quirk, workaround, deliberate tradeoff).

A code comment must **not** explain:

- Repository history (what was removed, renamed, migrated).
- Future plans (phases, roadmap items, "will be replaced").
- The author's thought process ("for now, assume...", "let's check...").
- What the next line of code literally does.
- Cross-cutting architectural rationale in long form (that's what `docs/architecture/` is for).

### Where Content Belongs

| Tier                                         | Carries                                                                |
| -------------------------------------------- | ---------------------------------------------------------------------- |
| **Code (comments)**                          | Current local truth, non-obvious invariants at the call site.          |
| **Commits (`git log`)**                      | History — what changed, when, why it changed.                          |
| **Issues / RFCs (`docs/rfcs/`, tracker)**    | Plans, proposals, open questions.                                      |
| **`docs/architecture/` + `docs/standards/`** | Cross-cutting decisions, architectural rationale, repo-wide standards. |

When a tempting comment doesn't fit the **Code** row, the correct move is usually to either delete it (the content belongs in one of the other tiers) or extract the load-bearing rationale into `docs/architecture/` and leave behind only the local one-liner.

## Anti-Patterns

### 1. History narration

Comments that describe what the code _used to be_ or _no longer does_. The current code already reflects the current state; the history is in `git log`.

| Bad                                                          | Better                                                      |
| ------------------------------------------------------------ | ----------------------------------------------------------- |
| `// Status check removed, now comes from Periscope.`         | _delete_ — or: `// Operational state comes from Periscope.` |
| `// V2 is now primary, V1 removed.`                          | `// Primary hook exports.`                                  |
| `// Kept for backward compatibility during migration.`       | `// Required by <caller>; field is optional on write.`      |
| `// Unlike the previous implementation, this uses channels.` | _delete_ (the code already shows channels).                 |

### 2. Roadmap narration

Phase/step/future-work comments. These rot instantly and mislead readers into thinking the code is incomplete when it isn't (or worse, that it's complete when it isn't).

| Bad                                    | Better                                         |
| -------------------------------------- | ---------------------------------------------- |
| `// Phase 3: compositor.`              | `// Compositor.`                               |
| `// TODO(phase-2): wire up telemetry.` | Open an issue. Link it if you must.            |
| `// Will be replaced once X lands.`    | _delete_ — or link a tracking issue.           |
| `// Step 1: fetch user.`               | _delete_ (function structure is self-evident). |

### 3. Stream-of-consciousness

Author inner monologue — uncertainty, self-questioning, conversational asides. These are a strong AI-generation signal.

| Bad                                                        | Better                                                                       |
| ---------------------------------------------------------- | ---------------------------------------------------------------------------- |
| `// For now, assume all codecs are available...`           | `// Real codec validation runs in initialize(); preselection is optimistic.` |
| `// Let's check if Cloudflare is reachable here.`          | `// Reachability probe — used to short-circuit retries.`                     |
| `// Not sure if this is needed but keeping it for safety.` | _delete_ or `// Required by <concrete constraint>.`                          |

### 4. Docs deflection

Comments that redirect to external docs **instead of** stating the local invariant. The reader is already here; they need the local fact, not a link chase.

| Bad                                            | Better                                                 |
| ---------------------------------------------- | ------------------------------------------------------ |
| `// See docs/architecture/viewer-routing.md`   | State the one invariant the reader needs at this site. |
| `// Refer to RFC-0042 for the full reasoning.` | Summarize the load-bearing constraint in one line.     |

Cross-linking is encouraged **in addition to** a local explanation — not as a substitute. The long-form rationale belongs in `docs/architecture/`; the local invariant belongs in the comment:

```go
// tenant_id is scoped from JWT at the middleware layer — never read from the payload.
// See docs/architecture/service-events.md for the full auth boundary.
```

### 5. Obvious restatement

Comments that paraphrase the next line.

| Bad                                                       | Better                                        |
| --------------------------------------------------------- | --------------------------------------------- |
| `// Create the user` above `user := ...`                  | _delete_                                      |
| `// Loop through items` above `for _, ...`                | _delete_                                      |
| `// Check if authenticated` above `if user.Authenticated` | _delete_                                      |
| `// Helper function for X`                                | _delete_ (the function name is the contract). |

### 6. Fake section headers

Large ASCII banners and numbered section markers are narration about structure, not explanation of behavior.

| Bad                                 | Better                                           |
| ----------------------------------- | ------------------------------------------------ |
| `// ========== HANDLERS ==========` | _delete_ (file layout is self-evident).          |
| `// --- Private helpers ---`        | _delete_ or use a real doc comment per function. |

## When a Comment _Is_ Warranted

Default to writing no comment. Add one when a future reader would be genuinely surprised by the code's behavior without it. Concrete triggers:

- **Protocol quirks.** `// MistServer uses MD5(MD5(password) + challenge) for auth.`
- **Ordering dependencies.** `// Must run before the tenant middleware — seeds the context key.`
- **Concurrency invariants.** `// Caller holds s.mu; do not re-acquire.`
- **Security invariants.** `// tenant_id comes from JWT, never from the payload.`
- **Non-obvious fallbacks.** `// Returns empty on timeout; callers treat that as "probe inconclusive".`
- **Cross-service contracts.** `// Registration triggers DNS sync via Kafka as a side-effect.`
- **Deliberate deviations from convention.** `// Retrying synchronously here because the caller is already on a goroutine.`

Good comments are short, specific, and tell the reader something the code cannot.

## API-Level Comments (proto, GraphQL, exported Go/TS)

These become user-facing documentation via codegen. The bar is higher, not lower:

- State the contract: inputs, outputs, units, error modes.
- Never narrate history or deprecation without a concrete replacement path.
- Never reference phases, migrations, or internal issue numbers.
- Document units explicitly when ambiguous (`bytes` vs `MiB`, seconds vs milliseconds).

Example (good, from `pkg/proto`):

```proto
// bandwidth_in is the cumulative bytes received since node start.
// Resets on restart. For rate, use up_speed (bytes/sec).
uint64 bandwidth_in = 3;
```

## Sweeping Existing Code

Greps for the anti-patterns above. Run against the repo, inspect each hit, delete or rewrite.

```
# History and roadmap narration
rg -n --type-add 'src:*.{go,ts,tsx,svelte,rs,proto,graphql,sql}' -tsrc \
   -e 'was removed' -e 'no longer' -e 'used to' -e 'kept for' \
   -e 'backward compat' -e 'legacy' -e 'Phase \d' -e 'Step \d' \
   -e 'will be replaced' -e 'TODO\(phase'

# Stream-of-consciousness
rg -n -tsrc -e 'for now' -e "let's" -e 'not sure' -e 'should probably' -e 'I think'

# Banners and obvious restatement
rg -n -tsrc -e '// ={5,}' -e '// -{5,}' -e '// Helper (function|method) for'
```

When a hit carries rationale worth keeping, lift it into `docs/architecture/` and leave a one-line cross-link at the call site.

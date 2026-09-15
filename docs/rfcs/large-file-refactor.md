# RFC: Large File Refactoring

## Status

Draft

## TL;DR

- 53 tracked source files exceed 1,500 lines and the largest is 11,066, so a flat
  1,500-line threshold is not a policy anyone can act on.
- Replace the threshold with a ratchet: no file may grow past its current size, and new
  files get a real cap.
- Nothing is enforced today; adopting this means adding the first size gate.

## Owning services / modules

Cross-cutting policy with no single owner. It applies to any service or package with
oversized files, and refactors land opportunistically alongside the owning service's own
changes, with coordinated updates to tests and imports.

## Current State

Every hand-written Go, TypeScript, and Svelte file over 3,000 lines, measured with
`wc -l` over `git ls-files` on 2026-09-15. Excluded: generated trees
(`pkg/proto/*.pb.go`, `api_gateway/graph/generated/`, `api_gateway/graph/model/`,
`website_application/$houdini/`), `_test.go` files, and
`infrastructure/demo-recordings/`, whose `.ts` files are MPEG transport stream
segments rather than TypeScript. `api_gateway/graph/schema.resolvers.go` is included
because gqlgen only scaffolds its signatures; the bodies are hand-written.

| Lines  | File                                                      |
| ------ | --------------------------------------------------------- |
| 11,066 | `api_balancing/internal/control/server.go`                |
| 10,763 | `api_tenants/internal/grpc/server.go`                     |
| 10,601 | `api_control/internal/grpc/server.go`                     |
| 8,748  | `cli/cmd/cluster_provision.go`                            |
| 8,552  | `api_analytics_query/internal/grpc/server.go`             |
| 7,511  | `api_balancing/internal/triggers/processor.go`            |
| 7,433  | `api_billing/internal/grpc/server.go`                     |
| 7,195  | `api_gateway/graph/schema.resolvers.go`                   |
| 5,493  | `npm_player/packages/core/src/core/PlayerController.ts`   |
| 5,062  | `api_balancing/internal/grpc/server.go`                   |
| 5,035  | `api_balancing/internal/state/stream_state.go`            |
| 4,992  | `api_gateway/internal/demo/generators.go`                 |
| 4,779  | `api_gateway/internal/resolvers/analytics_connections.go` |
| 4,492  | `api_sidecar/internal/control/client.go`                  |
| 4,227  | `api_analytics_ingest/internal/handlers/handlers.go`      |
| 3,936  | `api_sidecar/internal/handlers/poller.go`                 |
| 3,361  | `api_billing/internal/handlers/jobs.go`                   |
| 3,266  | `api_sidecar/internal/handlers/processing.go`             |

No size limit is enforced anywhere. `.golangci.yml` enables errcheck, govet,
ineffassign, staticcheck, unused, bodyclose, durationcheck, errorlint, nilerr, noctx,
sqlclosecheck, and depguard; none of them measures file length, and there is no
equivalent frontend rule.

## Problem / Motivation

Very large files increase cognitive load, slow reviews, and create merge conflicts.

The concrete problem with the previous version of this RFC is that its threshold was
unreachable. Telling authors to split at 1,500 lines when the top file is over seven
times that produces no behavior change: every touched file is already in violation, so
the rule carries no information about the change under review.

## Goals

- Stop the largest files from getting larger.
- Give new files a cap that is actually enforceable from day one.
- Keep refactors low-risk and package-local.

## Non-Goals

- Large-scale architectural changes.
- Cross-package moves or behavior changes.
- Any requirement to break up the existing files on a schedule.

## Proposal

**A per-file budget, not a global threshold.**

1. Snapshot the current line count of every hand-written file above the cap into a
   checked-in budget file.
2. A file listed in the budget may not exceed its recorded count. Adding lines to
   `api_balancing/internal/control/server.go` fails; moving lines out of it passes.
3. Budgets only move down. When a file shrinks, its new size becomes its budget, so a
   split cannot be silently undone later.
4. A file not in the budget is new and is capped at 1,500 lines.
5. Removing a file removes its budget entry.

This is the same shape as the existing lint baseline, where CI reports only violations
newer than the commit recorded in `.golangci-baseline`. Size needs per-file numbers
rather than a commit marker, but the principle is identical: old debt is frozen, new
debt is blocked.

Refactoring guidance, unchanged in spirit:

- Split along domain boundaries when they are clear; prefer same-package splits so no
  import or API changes are needed.
- Do not break up an orchestrator that has no clear seam just to satisfy a number. The
  ratchet never forces that, because it only reacts to growth.

## Impact / Dependencies

- Requires one new check (a script plus a Makefile target, wired into CI) and the
  checked-in budget file. That check does not exist today.
- Authors adding to a budgeted file must either move code out in the same change or
  land the addition in a new file.

## Alternatives Considered

- **Leave files as-is.** The status quo. Files keep growing with no signal.
- **Flat 1,500-line threshold.** Rejected: 53 tracked files already exceed it, and the
  largest five exceed it by three to seven times, so it flags existing code and blocks
  nothing new.
- **Full modular rewrites.** Too risky for the benefit, and out of scope here.
- **Advisory rule with no gate.** That is effectively what exists now, and it has not
  prevented any of the sizes above.

## Risks & Mitigations

- Risk: refactor introduces regressions. Mitigation: small, test-backed, package-local
  changes with no logic edits.
- Risk: authors dodge the ratchet by creating a thin second file. Mitigation: the
  1,500-line cap on new files, plus review; the goal is smaller units, not fewer lines.
- Risk: churn without value. Mitigation: only split when boundaries are clear; the
  ratchet never demands a split, only that the file not grow.

## Migration / Rollout

1. Add the budget file and the check, initially reporting only.
2. Make the check blocking once the budget is accurate.
3. Let per-service refactor PRs, with no logic changes, walk individual budgets down.

## Open Questions

- Is 1,500 the right cap for new files, or should Go and TypeScript differ?
- Should the budget cover test files, which are excluded above?

## References, Sources & Evidence

- Line counts: `wc -l` over the paths in the table, 2026-09-15.
- Absence of a size gate: `.golangci.yml`, `.github/workflows/ci.yml`.
- Baseline precedent: `.golangci-baseline` and the `lint` target in `Makefile`.

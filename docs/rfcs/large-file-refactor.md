# RFC: Large File Refactoring

## Status
Draft

## TL;DR
- Some files exceed maintainable size and slow reviews.
- Define a light policy for when to split files.
- Apply refactors opportunistically with low risk.

## Current State (as of 2026-01-13)
- Multiple TypeScript and Go files exceed 1,500+ lines.
- Large orchestrator files make reviews and navigation harder.

Evidence:
- `npm_player/packages/core/src/core/PlayerController.ts`
- `api_tenants/internal/grpc/server.go`
- `api_analytics_query/grpc/server.go`

## Problem / Motivation
Very large files increase cognitive load, slow reviews, and create merge conflicts.

## Goals
- Establish a simple refactor threshold and approach.
- Keep refactors low-risk and package-local.

## Non-Goals
- Large-scale architectural changes.
- Cross-package moves or behavior changes.

## Proposal
- When a file exceeds ~1,500–2,000 lines, split by domain boundaries if clear.
- Prefer same-package splits to avoid API changes.
- Do not refactor orchestrators unless clear boundaries exist.

## Impact / Dependencies
- Affects multiple services and packages.
- Requires coordinated changes in tests and imports.

## Alternatives Considered
- Leave files as-is.
- Full modular rewrites (too risky).

## Risks & Mitigations
- Risk: refactor introduces regressions. Mitigation: small, test-backed changes.
- Risk: churn without value. Mitigation: only split when boundaries are clear.

## Migration / Rollout
- Apply per-service refactor PRs with no logic changes.

## Open Questions
- Should there be an enforced size limit in CI?

## References, Sources & Evidence
- `npm_player/packages/core/src/core/PlayerController.ts`
- `api_tenants/internal/grpc/server.go`
- `api_analytics_query/grpc/server.go`

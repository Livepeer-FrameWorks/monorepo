# FrameWorks Agent Configuration

> Multi-tenant live streaming SaaS. Go microservices + SvelteKit + PostgreSQL/ClickHouse/Kafka.

## Demo Mode vs Seed Data

**These are completely separate systems. Identify which is relevant before acting.**

| Situation                        | System    | Target                                    |
| -------------------------------- | --------- | ----------------------------------------- |
| DB empty, missing data in dev    | Seed Data | `pkg/database/sql/seeds/`                 |
| Build failed, API sandbox broken | Demo Mode | `api_gateway/internal/demo/generators.go` |

## Where to Find Context

| Area                            | Location                                   |
| ------------------------------- | ------------------------------------------ |
| Architecture, services, ports   | `README.md`                                |
| Analytics pipeline, event types | `docs/architecture/analytics-pipeline.md`  |
| Service events backbone         | `docs/architecture/service-events.md`      |
| Viewer routing algorithm        | `docs/architecture/viewer-routing.md`      |
| Agent/wallet/x402/MCP           | `docs/architecture/agent-access.md`        |
| UI design system                | `docs/standards/design-system.md`          |
| Metrics naming/units            | `docs/standards/metrics.md`                |
| Deployment & ops                | `website_docs/src/content/docs/operators/` |
| Dev runtime                     | `docker-compose.yml`                       |
| Release pipeline                | `.github/workflows/release.yml`            |
| Workflows, Svelte 5 patterns    | `CONTRIBUTING.md`                          |

## Code Style

**Before committing:**

- Go: Run `make lint` to see all violations, `make lint-fix` for auto-fixes
- Frontend: Run `pnpm lint` and `pnpm format`

**Key rules:**

- Go uses tabs (enforced by `gofmt`)
- JS/TS/Svelte uses 2 spaces
- Unused variables: prefix with `_` (e.g., `_unused`)
- CI enforces linting on new code via baseline

## Building & Testing

**Always use the Makefile** - never use manual `go build` commands. See `Makefile` for all targets.

Common targets:

- `make build` - all services
- `make build-bin-<service>` - single service (e.g., `make build-bin-purser`)
- `make test` - run all tests
- `make verify` - full verification (tidy, fmt, vet, test, build)

## Prefer Scripts Over Manual Commands

**Always check for existing Makefile targets or `./scripts/` first.** Don't run raw tool invocations when a wrapper exists.

If a command requires multiple flags or is run repeatedly (e.g., `gremlins unleash ./pkg/auth --timeout-coefficient 5 --workers 4`), suggest creating a script:

```bash
# ./scripts/mutation-test.sh
./scripts/mutation-test.sh pkg/auth
```

Scripts provide: discoverable defaults, consistent flags, and documentation via code.

## Code Generation

- `make proto` - safe to run
- `make graphql` - **DO NOT RUN** (ask user to run locally; has zeroed resolvers in sandbox)
- After schema changes, ask user to run codegen before editing `api_gateway/graph/schema.resolvers.go`

## Never Edit Generated Code

| Generated (don't edit)           | Source (edit this)                |
| -------------------------------- | --------------------------------- |
| `pkg/proto/*.pb.go`              | `pkg/proto/*.proto`               |
| `api_gateway/graph/generated/*`  | `pkg/graphql/schema.graphql`      |
| `api_gateway/graph/model/*`      | `pkg/graphql/schema.graphql`      |
| `website_application/$houdini/*` | `pkg/graphql/operations/**/*.gql` |

## Service Boundaries

- Each service owns its schema; no cross-service DB reads
- Use `pkg/clients/` for cross-service data
- **All DB queries MUST filter by `tenant_id`** (JWT middleware extracts it)
- Kafka events carry `tenant_id` in headers, not payload

## Git: Read-Only

Never run: `reset`, `checkout <file>`, `clean`, `stash`, `revert`, `cherry-pick`

If changes need undoing, tell the user what to run.

## Behavioral Guidelines

**Ask, don't assume** - One clarifying question beats speculative changes

**Truth over appeasement** - Be honest about limitations; don't pretend stub services are implemented

**No AI narration in comments**:

- No `// Step 1:`, `// Phase 2:`, `// ========== SECTION ==========`
- No `// Helper function for X` (function name should be clear)
- No `// X was removed`, `// No longer needed`, `// Added in v2.0`
- No obvious restatements: `// Check if user is authenticated` above `if user.isAuthenticated`

Good comments explain WHY:

- `// MistServer uses MD5(MD5(password) + challenge) for auth`
- `// Registration triggers DNS sync as a side-effect`

**Avoid destructive commands without asking**: `git push --force`, deleting production data, modifying external repos

## Agent Workflow

**PLAN files** (`PLAN_*.md` in repo root): Working log for non-trivial tasks. Update progress, end with sign-off.

**RFC files** (`docs/rfcs/`): Proposals. If approved and implemented, merge content into canonical docs (`docs/architecture/`, `docs/standards/`, `website_docs/`) and delete the RFC.

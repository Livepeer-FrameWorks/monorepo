# Contributing to FrameWorks

## Development Setup

1. Clone: `git clone https://github.com/Livepeer-FrameWorks/monorepo.git`
2. Copy secrets: `cp config/env/secrets.env.example config/env/secrets.env`
3. Generate env: `make env`
4. Stage the media edge: `make edge-dev-dist` (needs Go and network access to the MistServer and Caddy release assets)
5. Start the stack: `docker compose up --build`

### Starting part of the stack

Services without a profile form the control plane: databases, Kafka, the Go services, the web apps and nginx.
Compose profiles add the rest. The generated `.env` sets `COMPOSE_PROFILES=edge` and lists the presets below as
comments; change that line, or set `COMPOSE_PROFILES` in your shell, to start a different slice.

| Preset                           | Starts                                                                           |
| -------------------------------- | -------------------------------------------------------------------------------- |
| `COMPOSE_PROFILES=`              | Control plane only                                                               |
| `COMPOSE_PROFILES=edge`          | Control plane and the media edge (default)                                       |
| `COMPOSE_PROFILES=edge,support`  | Also Chatwoot and Listmonk                                                       |
| `COMPOSE_PROFILES=edge,two-cell` | Also a second media cell (`foghorn-b`, `edge-b`); see `infrastructure/two-cell/` |
| `COMPOSE_PROFILES=llm`           | Control plane and a local Ollama runtime                                         |

- **Edge.** The `edge` service runs the production edge bundle (Caddy, MistServer and Helmsman under s6-overlay)
  built from `edge/Dockerfile` as `frameworks-edge:dev`. `make edge-dev-dist` stages Helmsman from your working tree
  and pinned MistServer and Caddy releases into `edge/dist/`. After changing Helmsman, run it again, then
  `docker compose up --build edge`. To test a local MistServer build, pass an install-tree tarball:
  `make edge-dev-dist EDGE_DEV_MIST_TAR=/path/to/mistserver.tar.gz` (`scripts/verify-mist-current-source.sh` writes one
  when `MIST_INSTALL_TREE_OUT` is set). nginx routes `/view/`, `/hls/`, `/webrtc/` and `/mist/` to the edge; RTMP
  (1935), SRT (8889/udp), RTSP (5554), DTSC (4200), WebRTC (18203/udp), Mist HTTP (8080) and the controller (4242)
  are published on the host.
- **Without the edge or support services.** nginx resolves upstreams per request, so their routes answer with the
  maintenance page. Deckhand, Commodore and Steward start without Chatwoot and Listmonk; they only call them while
  handling a support webhook or a newsletter subscription.
- `make verify-compose-profiles` runs `docker compose config` for every preset; CI runs it when the compose file or
  env generation changes.

## Code Style

### Linting

**All lint checks (CI parity):**

```bash
make lint
```

**Go:**

```bash
make lint-go    # Baseline-aware Go lint (same mode as CI go-lint)
make lint-all   # Show all Go violations (for cleanup)
make lint-fix   # Auto-fix what's possible
```

**Frontend:**

```bash
pnpm lint       # ESLint
pnpm format     # Prettier (auto-fix)
pnpm format:check  # Check without fixing
```

### Pre-commit Hooks

Hooks auto-install on `pnpm install` via Lefthook. They run:

- `gofmt` and `golangci-lint` on staged Go files
- Prettier and ESLint on staged frontend files

To skip temporarily: `git commit --no-verify`

### Indentation

- Go: tabs (enforced by `gofmt`)
- JS/TS/Svelte/JSON/YAML: 2 spaces
- See `.editorconfig` for full settings

### Svelte 5 Patterns

Use runes, not old syntax:

- `$state()` not `let`
- `$derived()` not `$:`
- `$props()` not `export let`

**Infinite loop gotcha**: Reading `$state` in an `$effect` condition causes loops even with `untrack`. Move the condition inside `untrack`:

```typescript
// Bad: myState.length outside untrack creates dependency
$effect(() => {
  if (newData && myState.length === 0) {
    untrack(() => {
      myState = newData;
    });
  }
});

// Good: check inside untrack
$effect(() => {
  if (newData) {
    untrack(() => {
      if (myState.length === 0) myState = newData;
    });
  }
});
```

## Running Tests

```bash
make test                              # All Go tests
cd api_control && go test ./... -v     # Specific service
cd website_application && pnpm test    # Frontend
make ci-local                          # Run core CI checks locally
make verify                            # Pre-commit verification
./scripts/mutation-test.sh pkg/auth/   # Mutation testing (validates test quality)
```

See `docs/standards/testing.md` for testing philosophy and best practices.

For CPU-, memory-, or database-heavy work, use the VPN-only shared build host with an isolated task
slot. See [Remote development](docs/development/remote-development.md); the short path is
`scripts/remote-dev.sh sync <slot>` followed by `scripts/remote-dev.sh run <slot> make <target>`.

## Release Build Infrastructure

The release pipeline (`.github/workflows/release.yml`) uses two runner types:

- **`ubuntu-latest`** (GitHub-hosted): builds linux/amd64 binaries and Docker images
- **`macos-arm64-self-hosted`** (Mac Mini): builds linux/arm64 + darwin/arm64 binaries, handles Apple code signing and notarization

The Mac Mini runner requires:

- GitHub Actions runner agent (configured as LaunchDaemon)
- Go 1.25+
- `filosottile/musl-cross/musl-cross` (Homebrew) for CGO linux cross-compilation
- Apple Developer certificate (imported via secrets)
- Docker Desktop (for future arm64 container image builds)

## Common Workflows

### Adding a New GraphQL Field

```bash
# 1. Edit schema
vim pkg/graphql/schema.graphql

# 2. Run codegen: gateway, webapp, tray app, the three SDKs and the docs API
#    reference, all from the schema. A change that starts a new SDK line
#    (docs/standards/graphql-deprecation.md) needs `pnpm version-packages`
#    first; codegen says so when it does.
make graphql-all

# 3. Implement resolver stub
vim api_gateway/graph/schema.resolvers.go

# 4. Add/update frontend operations
vim pkg/graphql/operations/queries/MyQuery.gql

# 5. If schema changed, update demo generators
vim api_gateway/internal/demo/generators.go
```

### Adding a New Event Type

See `docs/architecture/analytics-pipeline.md` section "Extending Analytics" for the full checklist. Summary:

```bash
# 1. Define protobuf message
vim pkg/proto/ipc.proto

# 2. Generate Go code
make proto

# 3. Emit the event from producing service (Helmsman/Foghorn/etc.)
vim api_balancing/internal/triggers/processor.go  # or relevant service

# 4. Update ClickHouse schema
vim pkg/database/sql/clickhouse/periscope.sql

# 5. Add ingest handler to write to ClickHouse
vim api_analytics_ingest/internal/handlers/handlers.go

# 6. Add query method if exposing via API
vim api_analytics_query/internal/grpc/server.go

# 7. If exposing to frontend: update GraphQL schema + resolvers + demo generators
```

## Pull Requests

- Branch from `development`
- One feature or fix per PR
- Keep PRs under 800 lines where possible — split large changes into reviewable chunks
- Architectural changes require an RFC in `docs/rfcs/` first
- Fully completed RFC's require merging into generic `/docs` architectural documents
- Fill out the PR template
- Make sure CI passes

## AI-Assisted Development

AI is part of our toolchain — we use it for development, code review, and issue triage. It's a tool, not an author. Every change goes through the same review and testing process regardless of how it was written.

Contributors are free to use AI tools. Automated commits (e.g., from Codex) carry a `Co-Authored-By` trailer for transparency.

## RFCs

Significant changes need an RFC first:

1. Create `docs/rfcs/your-feature.md` (use `docs/rfcs/RFC_TEMPLATE.md`)
2. Open a PR for discussion
3. Get approval before implementing

## Questions?

Open a [Discussion](https://github.com/Livepeer-FrameWorks/monorepo/discussions).

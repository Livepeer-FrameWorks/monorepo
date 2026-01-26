# Contributing to FrameWorks

## Development Setup

1. Clone: `git clone https://github.com/Livepeer-FrameWorks/monorepo.git`
2. Copy secrets: `cp config/env/secrets.env.example config/env/secrets.env`
3. Generate env: `make env`
4. Start stack: `docker-compose up`

## Code Style

- Go: Run `make verify` (includes `gofmt`, `golangci-lint`, tests)
- TypeScript/Svelte: Run `pnpm lint`

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
    untrack(() => { myState = newData; });
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
make verify                            # Pre-commit verification
```

## Common Workflows

### Adding a New GraphQL Field

```bash
# 1. Edit schema
vim pkg/graphql/schema.graphql

# 2. Run codegen
make graphql

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
- Fill out the PR template
- Make sure CI passes

## RFCs

Significant changes need an RFC first:

1. Create `docs/rfcs/your-feature.md` (use `docs/rfcs/RFC_TEMPLATE.md`)
2. Open a PR for discussion
3. Get approval before implementing

## Questions?

Open a [Discussion](https://github.com/Livepeer-FrameWorks/monorepo/discussions).

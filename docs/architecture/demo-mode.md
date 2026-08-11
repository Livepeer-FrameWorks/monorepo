# Demo Mode - Synthetic API Sandbox Boundary

Demo mode lets anyone exercise the full GraphQL API — including the webapp's API
explorer — against a synthetic tenant without an account, without credentials, and
without any backend service holding real data for it. It is request-scoped and
generator-backed: nothing is provisioned, and demo responses are fabricated
in-process by Bridge.

## Activation

`DemoModePostAuth` (Bridge middleware, mounted on the `/graphql` group **after**
`PublicOrJWTAuth`) arms demo mode when any of these is present:

- header `X-Demo-Mode: true` (literal `"true"` only — `1`/`yes` do not arm it)
- query parameter `?demo=true`
- a User-Agent containing `API-Explorer-Demo`

The webapp API explorer (`website_application/src/lib/graphql/services/explorer.ts`)
sends `X-Demo-Mode: true` when the user flips its demo toggle.

On a demo request the middleware sets:

| Context key       | Value                                              |
| ----------------- | -------------------------------------------------- |
| `KeyDemoMode`     | `true`                                             |
| `KeyDemoTenantID` | `5eed517e-ba5e-da7a-517e-ba5eda7a0001` (synthetic) |
| `KeyDemoUserID`   | `5eedface-5e1f-da7a-face-5e1fda7a0001` (synthetic) |
| `KeyReadOnly`     | `true`                                             |

It deliberately does **not** set `KeyTenantID` or `KeyUserID`. Those stay empty so
the demo caller is classified as an anonymous/public caller by rate limiting
(per-IP public throttle). Injecting the demo IDs into the real credential slots
would let anyone bypass tenant rate limits with a header.

## What is generated vs real

Every resolver that supports demo checks `middleware.IsDemoMode(ctx)` **before**
calling any backend client and returns data from `api_gateway/internal/demo`
instead (checks exist across `api_gateway/internal/resolvers/*` — streams, billing,
analytics connections, infrastructure, VOD, platform admin, subscriptions).

- **Generated:** everything. `internal/demo/generators.go` and
  `generators_platform.go` synthesize proto/GraphQL payloads in memory — streams,
  invoices, usage, analytics time series, cluster/node inventory, marketplace
  state, subscription event streams — around fixed IDs (`DemoStreamID`,
  `DemoPlaybackID`, clusters `central-primary`/`demo-media`, …).
- **Real:** nothing. No backend gRPC call, no database row, no Kafka event.
  Mutations return simulated success (e.g. `createStream` fabricates a stream,
  `deleteStream` returns `DeleteSuccess`) without touching Commodore.
- **Unavailable:** root fields with no demo representation return
  `errDemoUnavailable` (`internal/resolvers/demo_support.go`) rather than falling
  through to a backend that would only produce an opaque auth/transport error.

The usage tracker skips demo requests entirely (no observability value), so demo
traffic never lands in `api_request_batch` metering.

## Boundary rule

**Demo data never crosses into real tenant paths.** The synthetic tenant/user IDs
exist only inside demo-mode request contexts and generator constants — they are
never provisioned in Commodore/Quartermaster/Purser. The boundary is enforced by
construction:

1. Real credential slots stay empty, so a resolver that misses its demo check
   fails backend authentication instead of leaking another tenant's data.
2. Demo-path resolvers return before any client call, so demo mutations cannot
   create real state.
3. `KeyReadOnly` marks the context as a safety signal for anything downstream.

The inverse also holds: real tenants can never receive generated data, because
generators are only reachable behind `IsDemoMode(ctx)`.

## Relationship to seed data

Demo mode and seed data are separate systems (see the repo `CLAUDE.md` table).
`pkg/database/sql/seeds/demo/` (`demo_data.sql`, `clickhouse_demo_data.sql`) loads
dev-compose-only rows that reuse the same synthetic identifiers (demo tenant,
`demo_live_stream_001`, `central-primary`/`demo-media` clusters) so the _real_
resolver paths have data to return during local development. Demo mode never reads
those rows; in production they do not exist and demo mode still works, because it
never touches storage.

## Contract tests

`api_gateway/graph/demo_schema_sweep_test.go` executes every schema field in demo
mode; the `mustSucceedQueries` list hard-asserts that sandbox-critical fields
(streams, billing, clusters, …) return data with no errors — a regression there
means the API explorer sandbox is broken.
`api_gateway/internal/middleware/demo_test.go` pins the activation triggers and
the "real credential slots stay nil" invariant.

## Key Files

- `api_gateway/internal/middleware/demo.go` - activation triggers, context keys, credential-slot invariant
- `api_gateway/internal/demo/generators.go` - synthetic data generators (streams, billing, analytics, infra)
- `api_gateway/internal/demo/generators_platform.go` - platform-operator view generators
- `api_gateway/internal/resolvers/demo_support.go` - `errDemoUnavailable` for fields with no demo path
- `api_gateway/graph/demo_schema_sweep_test.go` - full-schema demo sweep + hard success assertions
- `pkg/database/sql/seeds/demo/` - dev-only seeds that reuse the same synthetic IDs (separate system)

## Gotchas

- Demo mode is per-request, not per-session: dropping the header/parameter mid
  "session" silently switches to real (usually unauthenticated) behavior.
- Demo requests are rate-limited as anonymous public traffic on purpose. Fixing a
  "demo is rate limited" complaint by injecting the demo tenant into `KeyTenantID`
  would open the bypass described above.
- Adding a new root field without either a generator path or an
  `errDemoUnavailable` return degrades the sandbox — the schema sweep test
  surfaces this.

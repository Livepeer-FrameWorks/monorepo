# Environment Configuration

## Current Shape

As of the April 2026 audit, the repo reads about 295 unique environment variables in code and declares about 339 across `.env`, `config/env/*.env`, and frontend example files.

That surface is not all "real" configuration. A large part of it is derived or duplicated:

- `config/env/base.env` is the canonical non-secret topology and public URL input.
- `config/env/secrets.env` is the canonical secret and operator-supplied input.
- `pkg/configgen/configgen.go` derives `.env` values such as `DATABASE_URL`, `KAFKA_BROKERS`, `*_URL`, `*_GRPC_ADDR`, and `VITE_*`.
- `docker-compose.yml` then remaps parts of that generated `.env` into per-container generic names such as `PORT`, `GRPC_PORT`, `KAFKA_CLIENT_ID`, and `KAFKA_GROUP_ID`.

The first rule should be: treat `.env` as generated output, not as a hand-maintained source of truth.

## Canonical Layers

Use these layers when adding or reviewing config:

| Layer                          | Purpose                                            | Examples                                                                          |
| ------------------------------ | -------------------------------------------------- | --------------------------------------------------------------------------------- |
| Canonical base input           | Shared topology, public URLs, non-secret defaults  | `POSTGRES_HOST`, `QUARTERMASTER_HOST`, `GATEWAY_PUBLIC_URL`, `STREAMING_EDGE_URL` |
| Canonical secret input         | Credentials, API keys, operator-only values        | `JWT_SECRET`, `SERVICE_TOKEN`, `STRIPE_SECRET_KEY`, `CLOUDFLARE_API_TOKEN`        |
| Derived output                 | Computed from canonical input                      | `DATABASE_URL`, `KAFKA_BROKERS`, `COMMODORE_GRPC_ADDR`, `VITE_GRAPHQL_HTTP_URL`   |
| Service-local runtime override | Only when one service genuinely needs its own knob | `PRIVATEER_SYNC_INTERVAL`, `DECKLOG_METRICS_PORT`, `SKIPPER_SOCIAL_INTERVAL`      |

If a variable can be derived deterministically, prefer deriving it over documenting another editable key.

## Main Duplication Buckets

### Internal service discovery

The repo carries the same service identity in several shapes:

- host vars such as `COMMODORE_HOST`
- port vars such as `COMMODORE_PORT` and `COMMODORE_GRPC_PORT`
- derived URLs such as `COMMODORE_URL`
- derived addresses such as `COMMODORE_GRPC_ADDR`

This is mostly fine if only host and port are editable. It becomes noisy when derived forms appear alongside their sources in generated env or docs.

Recommendation:

- Keep `*_HOST` and `*_PORT` or `*_GRPC_PORT` as canonical inputs.
- Derive `*_URL` and `*_GRPC_ADDR` only.
- Avoid adding new handwritten `*_URL` or `*_GRPC_ADDR` entries outside config generation.

### Runtime mode flags

The code checks several overlapping flags for "dev vs prod":

- `BUILD_ENV`
- `NODE_ENV`
- `GO_ENV`
- `ENV`
- `GIN_MODE`

Recommendation:

- Use `BUILD_ENV` as the shared app/runtime environment flag.
- Keep `NODE_ENV` only where frontend tooling expects it.
- Stop adding new uses of `ENV` and `GO_ENV`.
- Use `GIN_MODE` only for Gin behavior, not as the repo-wide environment selector.

### gRPC TLS and insecure toggles

There is a strong shared set already:

- `GRPC_ALLOW_INSECURE`
- `GRPC_TLS_CA_PATH`
- `GRPC_TLS_CERT_PATH`
- `GRPC_TLS_KEY_PATH`
- `GRPC_TLS_SERVER_NAME`

There are also service-specific aliases such as `DECKLOG_ALLOW_INSECURE`, `DECKLOG_TLS_*`, `FOGHORN_GRPC_TLS_*`, and `HELMSMAN_GRPC_TLS_*`.

Recommendation:

- Prefer the shared `GRPC_*` keys in application code.
- Keep service-specific names only at provisioning or compose boundaries when they map into shared runtime keys.
- Do not add new service-specific TLS names unless the service has truly different semantics.

### Frontend/public URL mirrors

`configgen` already derives many browser-facing variables:

- `GATEWAY_PUBLIC_URL` -> `VITE_GATEWAY_URL`, `VITE_GRAPHQL_HTTP_URL`, `VITE_GRAPHQL_WS_URL`, `VITE_MCP_URL`, `VITE_WEBHOOKS_URL`
- `WEBAPP_PUBLIC_URL` -> `VITE_APP_URL`
- `MARKETING_PUBLIC_URL` -> `VITE_MARKETING_SITE_URL`
- `DOCS_PUBLIC_URL` -> `VITE_DOCS_SITE_URL`
- `FORMS_PUBLIC_URL` -> `VITE_CONTACT_API_URL`

Recommendation:

- Treat non-`VITE_` public URLs as canonical.
- Treat `VITE_*` as build outputs.
- Prefer a single public URL family in docs and examples, then derive frontend aliases.

### Kafka client/group wrappers

Several services read generic `KAFKA_CLIENT_ID` and `KAFKA_GROUP_ID`, but the generated env still carries service-specific wrappers such as `SIGNALMAN_KAFKA_CLIENT_ID` and `PURSER_KAFKA_GROUP_ID`.

Recommendation:

- Keep service defaults in code.
- Only override generic `KAFKA_CLIENT_ID` or `KAFKA_GROUP_ID` when needed.
- Avoid expanding the number of per-service Kafka wrapper variables.

## High-Value Cleanup Targets

These are the best no-behavior-change cleanup candidates:

1. Document `.env` as generated and stop treating it as an editable contract.
2. Replace direct dev/prod checks against `ENV` and `GO_ENV` with a shared helper or `BUILD_ENV`.
3. Document missing but real shared keys in examples: `GRPC_ALLOW_INSECURE`, `GRPC_TLS_*`, `ACME_ENV`, `CERT_ISSUANCE_TOKEN`, `EXPECTED_INTERNAL_GRPC_SERVICES`, and Privateer mesh settings.
4. Trim env-file-only drift that is not read by application code and is not a configgen source. Review items like unused compose-only wrappers separately from real dead keys.
5. Keep feature-heavy domains isolated: Skipper AI, x402/crypto settlement, Navigator CA import, and Privateer mesh should not leak more shared globals than necessary.

## Concrete Keep / Derive / Phase Out

### Keep as canonical editable inputs

These should remain the human-edited source of truth:

- Topology: `POSTGRES_HOST`, `POSTGRES_PORT`, `POSTGRES_DB`, `CLICKHOUSE_HOST`, `CLICKHOUSE_HTTP_PORT`, `CLICKHOUSE_NATIVE_PORT`, `KAFKA_HOST`, `KAFKA_PORT`
- Public URLs: `GATEWAY_PUBLIC_URL`, `AUTH_PUBLIC_URL`, `WEBAPP_PUBLIC_URL`, `MARKETING_PUBLIC_URL`, `DOCS_PUBLIC_URL`, `FORMS_PUBLIC_URL`
- Service placement: `*_HOST`, `*_PORT`, `*_GRPC_PORT`
- Shared runtime: `BUILD_ENV`, `GIN_MODE`, `LOG_LEVEL`, `ALLOWED_ORIGINS`, `TRUSTED_PROXY_CIDRS`
- Shared secrets: `JWT_SECRET`, `PASSWORD_RESET_SECRET`, `SERVICE_TOKEN`, `FIELD_ENCRYPTION_KEY`
- Shared TLS: `GRPC_ALLOW_INSECURE`, `GRPC_TLS_CA_PATH`, `GRPC_TLS_CERT_PATH`, `GRPC_TLS_KEY_PATH`, `GRPC_TLS_SERVER_NAME`

### Derive instead of editing directly

These are outputs and should not be treated as first-class editable config:

- `DATABASE_URL`
- `KAFKA_BROKERS`
- `COMMODORE_URL`, `QUARTERMASTER_URL`, `PURSER_URL`, `PERISCOPE_QUERY_URL`, `PERISCOPE_INGEST_URL`, `MISTSERVER_URL`, `FOGHORN_URL`, `HELMSMAN_WEBHOOK_URL`
- `COMMODORE_GRPC_ADDR`, `QUARTERMASTER_GRPC_ADDR`, `PURSER_GRPC_ADDR`, `PERISCOPE_GRPC_ADDR`, `SIGNALMAN_GRPC_ADDR`, `DECKHAND_GRPC_ADDR`, `SKIPPER_GRPC_ADDR`
- `FOGHORN_CONTROL_ADDR`, `FOGHORN_CONTROL_BIND_ADDR`
- `VITE_GATEWAY_URL`, `VITE_GRAPHQL_HTTP_URL`, `VITE_GRAPHQL_WS_URL`, `VITE_MCP_URL`, `VITE_WEBHOOKS_URL`
- `VITE_AUTH_URL`, `VITE_APP_URL`, `VITE_MARKETING_SITE_URL`, `VITE_DOCS_SITE_URL`, `VITE_CONTACT_API_URL`
- `VITE_STREAMING_*`
- `BASE_PATH`, `DOCS_BASE_PATH`

### Phase out as duplicate environment selectors

Use `BUILD_ENV` as the repo-wide selector. Keep `NODE_ENV` only where frontend tooling requires it.

- `ENV`
- `GO_ENV`
- most direct app logic reads of `NODE_ENV`

### Phase out as service-specific TLS aliases

These are acceptable as compose/provisioning shims, but application code should prefer shared `GRPC_*` keys:

- `DECKLOG_ALLOW_INSECURE`
- `DECKLOG_TLS_CERT_FILE`
- `DECKLOG_TLS_KEY_FILE`
- `FOGHORN_GRPC_TLS_CERT_PATH`
- `FOGHORN_GRPC_TLS_KEY_PATH`
- `HELMSMAN_GRPC_TLS_CERT_PATH`
- `HELMSMAN_GRPC_TLS_KEY_PATH`

### Remove from base env if service defaults are good enough

These currently exist mostly to feed generic runtime keys that already have service defaults in code:

- `SIGNALMAN_KAFKA_CLIENT_ID`
- `SIGNALMAN_KAFKA_GROUP_ID`
- `PURSER_KAFKA_CLIENT_ID`
- `PURSER_KAFKA_GROUP_ID`
- `PERISCOPE_INGEST_KAFKA_CLIENT_ID`
- `PERISCOPE_INGEST_KAFKA_GROUP_ID`

### Add to examples/docs because code really reads them

These are real runtime knobs but are missing or under-documented in env examples:

- `GRPC_ALLOW_INSECURE`
- `GRPC_TLS_CA_PATH`
- `GRPC_TLS_CERT_PATH`
- `GRPC_TLS_KEY_PATH`
- `GRPC_TLS_SERVER_NAME`
- `ACME_ENV`
- `CERT_ISSUANCE_TOKEN`
- `EXPECTED_INTERNAL_GRPC_SERVICES`
- `PRIVATEER_CERT_SYNC_INTERVAL`
- `PRIVATEER_SYNC_INTERVAL`
- `PRIVATEER_SYNC_TIMEOUT`
- `MESH_EXTERNAL_IP`
- `MESH_INTERNAL_IP`
- `MESH_INTERFACE`
- `MESH_LISTEN_PORT`
- `MESH_NODE_NAME`
- `MESH_NODE_TYPE`
- `UPSTREAM_DNS`
- `TURNSTILE_FAIL_OPEN`

### Naming collisions to fix

These are especially confusing because the same key changes meaning across layers:

- `CLICKHOUSE_HOST`: canonical input is a host name in `base.env`, but config generation rewrites it into `host:port` runtime form
- `NAVIGATOR_URL`: despite the name, runtime code uses it as a bare `host:port` gRPC address rather than a URL

## Repeatable Audit

Run:

```bash
make env-audit
```

That script reports:

- shared variables used across services
- per-service backend and frontend inventories
- keys declared in env files but not read by app code
- keys read in code but missing from env/example files

## Practical Rule

When adding a new variable, decide this first:

1. Is it canonical operator input?
2. Can it be derived from existing canonical input?
3. Is it only a service-local override?

If the answer is "derived", it should not become another hand-maintained env key.

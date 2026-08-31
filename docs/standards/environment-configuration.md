# Environment Configuration

## Current Shape

As of the April 2026 audit, a lightweight scan finds about 290 unique environment variables read in code and about 287 declared across `.env`, `config/env/*.env`, and frontend example files. Treat those numbers as drift indicators, not a contract: configgen output, shell scripts, compose interpolation, and generated examples can move the count without changing the supported operator surface.

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
| Service-local runtime override | Only when one service genuinely needs its own knob | `DECKLOG_METRICS_PORT`, `SKIPPER_SOCIAL_INTERVAL`                                 |

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
- Keep service-to-service dependency facts in `pkg/topology/dependencies.go`;
  provisioning, mesh DNS, and runtime reconciliation should consume that
  catalog instead of adding service-specific discovery knobs.

### Runtime mode flags

- `BUILD_ENV`
- `NODE_ENV`
- `GIN_MODE`

Recommendation:

- Use `BUILD_ENV` as the shared app/runtime environment flag.
- Keep `NODE_ENV` only where frontend tooling expects it.
- Use `GIN_MODE` only for Gin behavior, not as the repo-wide environment selector.

### gRPC TLS and insecure toggles

There is a strong shared set already:

- `GRPC_ALLOW_INSECURE`
- `GRPC_METADATA_POLICY` (`allow`, `audit`, or `deny`; Foghorn deploys with `deny`)
- `GRPC_TLS_CA_PATH`
- `GRPC_TLS_CERT_PATH`
- `GRPC_TLS_KEY_PATH`

Recommendation:

- Prefer shared `GRPC_*` keys for TLS material and insecure dev/test policy.
- Use service-specific TLS authority names, for example
  `QUARTERMASTER_GRPC_TLS_SERVER_NAME`, when a process dials more than one gRPC
  service. A single process-wide ServerName is not valid for multi-client
  services.

### Cluster-access signing secrets

- `CLUSTER_ACCESS_MATERIALIZATION_SECRET` is rendered only to Quartermaster and
  Purser. It authenticates the narrow commercial/owner grant materialization
  and revocation envelopes; it is not a general service credential. Both
  services refuse to start without it. Local Compose supplies an explicit
  development-only fallback; non-development manifests must inject a shared,
  generated value.
- `FOGHORN_BALANCER_CAPABILITY_SECRET` is rendered to every Foghorn replica. It signs
  short-lived, node/cluster-bound public source-lookup URLs used by Mist. The
  capability is carried in the URL path because Mist replaces configured query
  parameters; rotation updates only managed stream sources, not the full edge
  configuration seed. Every HA replica must share the same secret or requests
  minted by one replica will fail when load-balanced to another.
- Local Compose supplies explicit development-only fallbacks for both secrets.
  Provisioning generates them when absent in development, while
  non-development manifests require shared secret inputs. Do not copy either
  key into tenant-hosted edge environments.

### Media-authority signing keys

- `MEDIA_AUTHORITY_SIGNING_KEY_ID` and
  `MEDIA_AUTHORITY_SIGNING_PRIVATE_KEY_PEM_B64` are rendered only to Commodore. The private value
  is one base64-wrapped PKCS#8 Ed25519 PEM; there is no service-token or HMAC fallback.
- `MEDIA_AUTHORITY_TRUST_SET` is rendered only to Foghorn and is a JSON object mapping accepted key
  IDs to base64 raw Ed25519 public keys. An overlap set may contain current and next public keys;
  it never contains the private signer.
- Development provisioning generates one matching signer/trust tuple. Non-development
  provisioning requires a complete matching tuple and rejects partial or mismatched values.
  Local Compose has a fixed development-only tuple so restarting the stack preserves signatures.
  For a fresh deployment, operators generate the complete production shared-secret set with
  `frameworks cluster secrets generate-shared --out <new-file>`. For a v0.3.0 upgrade of an
  existing deployment, `frameworks cluster secrets generate-media-authority --out <new-file>`
  emits only `MEDIA_AUTHORITY_SIGNING_KEY_ID`,
  `MEDIA_AUTHORITY_SIGNING_PRIVATE_KEY_PEM_B64`, `MEDIA_AUTHORITY_TRUST_SET`,
  `MEDIA_AUTHORITY_SEAL_ROOT_SECRET`, and `FOGHORN_STATE_ENCRYPTION_KEY`, so unrelated existing
  credentials are not rotated. Import the chosen fragment into the SOPS-managed shared secret
  source. Lifecycle commands validate but never silently create or persist production keys.
- `MEDIA_AUTHORITY_SEAL_ROOT_SECRET` is a 32-byte hex deployment credential. The CLI uses HKDF to
  derive a distinct X25519 recipient for every declared control cell. The root is never rendered
  into a workload: Commodore receives only `MEDIA_AUTHORITY_SEAL_RECIPIENTS`, while each Foghorn
  receives only its cell's `MEDIA_AUTHORITY_SEAL_KEY_ID` and
  `MEDIA_AUTHORITY_SEAL_PRIVATE_KEY_PEM_B64`. This keeps pull URIs, native sources, webhook
  secrets, and multistream destinations out of the generally readable signed envelope and prevents
  one cell from decrypting another cell's section. The key ID is the truncated SHA-256 binding of
  control-cell ID and raw X25519 public key; both producer and consumer recompute it at startup.
  Treat these variables as renderer output rather than independently editable values.
- Rotating the seal root changes every derived recipient. v0.3.0 supports one active recipient per
  cell, so rotation is a coordinated maintenance operation: stop authority compilation, replace
  the rendered keys, reissue every secret-bearing authority, and verify cell convergence before
  relying on outage mode. Do not rotate while bundles encrypted only to the old key must remain
  usable through an outage.

### Frontend/public URL mirrors

`configgen` already derives many browser-facing variables:

configgen derives all `VITE_*` from canonical public URLs:

- `GATEWAY_PUBLIC_URL` -> `VITE_GATEWAY_URL`, `VITE_GRAPHQL_HTTP_URL`, `VITE_GRAPHQL_WS_URL`, `VITE_MCP_URL`, `VITE_WEBHOOKS_URL`, `VITE_AUTH_URL`
- `WEBAPP_PUBLIC_URL` -> `VITE_APP_URL`
- `MARKETING_PUBLIC_URL` -> `VITE_MARKETING_SITE_URL`
- `DOCS_PUBLIC_URL` -> `VITE_DOCS_SITE_URL`
- `FORMS_PUBLIC_URL` -> `VITE_CONTACT_API_URL`
- `STREAMING_INGEST_URL` -> `VITE_STREAMING_INGEST_URL`
- `STREAMING_PLAY_URL` -> `VITE_STREAMING_PLAY_URL`
- `STREAMING_EDGE_URL` -> `VITE_STREAMING_EDGE_URL`
- `FROM_EMAIL` -> `VITE_CONTACT_EMAIL`
- `TURNSTILE_AUTH_SITE_KEY` -> `VITE_TURNSTILE_AUTH_SITE_KEY`
- `TURNSTILE_FORMS_SITE_KEY` -> `VITE_TURNSTILE_FORMS_SITE_KEY`

Product constants (GITHUB_URL, LIVEPEER_URL, streaming ports/paths, BRAND_NAME, etc.) are hardcoded in `packages/site-config/index.ts`, not derived from env.

### Kafka client/group wrappers

Services use generic `KAFKA_CLIENT_ID` and `KAFKA_GROUP_ID`, with service-specific defaults kept in code.

Recommendation:

- Keep service defaults in code.
- Only override generic `KAFKA_CLIENT_ID` or `KAFKA_GROUP_ID` when needed.
- Do not add per-service Kafka wrapper variables back into base env, compose, or generated env.

## High-Value Cleanup Targets

### Media-cell durable state

- `HELMSMAN_STATE_DIR` is required durable local state, separate from
  `HELMSMAN_STORAGE_LOCAL_PATH`. It owns ConfigSeed, trigger WAL, ingest fences,
  node identity, processing overrides, and the media-control completion outbox.
  Container production maps it to `/data/state`; native roles render the
  platform-specific persistent path.
- `FRAMEWORKS_TRIGGER_WAL_DIR` is an optional explicit override beneath a
  durable filesystem. When omitted, Helmsman uses
  `<HELMSMAN_STATE_DIR>/trigger-wal`; there is no cache or `/tmp` fallback.
- `FRAMEWORKS_CONTROL_OUTBOX_DIR` optionally overrides the completion outbox;
  production uses `<HELMSMAN_STATE_DIR>/control-outbox`. The control package has
  a user-cache/temporary fallback for direct library and test use, but Helmsman
  production startup rejects an empty `HELMSMAN_STATE_DIR` before that path can
  be used.
- In the shared deployment secret input, `FOGHORN_STATE_ENCRYPTION_KEY` is a
  32-byte hex root. The CLI derives a distinct cell key with HKDF and renders
  that result under the same variable name only to Foghorn. Replicas in one
  control cell receive the same derived key; different cells do not. This lets
  HA replicas read credential-bearing local obligations without giving one
  cell the key for another cell. Admission payloads are v2 row-bound
  ciphertext. Foghorn automatically migrates legacy plaintext/v1 obligations;
  `foghorn_admission_payload_crypto_total{format,result}` is the completion
  signal, not an operator-managed compatibility flag.
- Production lifecycle commands require stable operator-supplied shared
  secrets. Development provisioning generates missing shared secrets once and
  stores only those generated values in
  `.frameworks/dev-generated-secrets.env` beside the local manifest with mode
  `0600`; later provision/apply/diff/upgrade/restart invocations reuse them.
- `HELMSMAN_ROTATE_NODE_IDENTITY` is not a steady-state operator toggle. The
  provisioning flow renders it only for an explicit token-backed
  `--force-reenroll` recovery. Helmsman binds that request to a hash of the
  fresh enrollment token, keeps the replacement key stable across retries,
  and records completion after Foghorn accepts the registration. A persisted
  flag with the same token cannot rotate the key again on restart.

These are the best no-behavior-change cleanup candidates:

1. Document `.env` as generated and stop treating it as an editable contract.
2. Keep backend runtime checks on `BUILD_ENV` and avoid introducing parallel environment selectors.
3. Keep real operator-owned keys covered by examples and operator docs: shared gRPC TLS keys, `ACME_ENV`, `CERT_ISSUANCE_TOKEN`, `FEDERATION_ENABLED`, `RERANKER_API_URL`, `TURNSTILE_FAIL_OPEN`, and mesh-specific TLS names such as `QUARTERMASTER_GRPC_TLS_SERVER_NAME` / `NAVIGATOR_GRPC_TLS_SERVER_NAME`.
4. Trim env-file-only drift that is not read by application code and is not a configgen source. Review items like unused compose-only wrappers separately from real dead keys.
5. Keep feature-heavy domains isolated: Skipper AI, x402/crypto settlement, Navigator CA import, and Privateer mesh should not leak more shared globals than necessary.

## Concrete Keep / Derive / Phase Out

### Keep as canonical editable inputs

These should remain the human-edited source of truth:

- Topology: `POSTGRES_HOST`, `POSTGRES_PORT`, `POSTGRES_DB`, `CLICKHOUSE_HOST`, `CLICKHOUSE_HTTP_PORT`, `CLICKHOUSE_NATIVE_PORT`, `KAFKA_HOST`, `KAFKA_PORT`
- Public URLs: `GATEWAY_PUBLIC_URL`, `WEBAPP_PUBLIC_URL`, `MARKETING_PUBLIC_URL`, `DOCS_PUBLIC_URL`, `FORMS_PUBLIC_URL`
- Billing routing: `PAYMENT_CARD_PROVIDER` (`stripe` or `mollie`) when both providers are fully configured; omit it when only one is ready
- Service placement: `*_HOST`, `*_PORT`, `*_GRPC_PORT`
- Shared runtime: `BUILD_ENV`, `GIN_MODE`, `LOG_LEVEL`, `ALLOWED_ORIGINS`, `TRUSTED_PROXY_CIDRS`
- Shared secrets: `JWT_SECRET`, `PASSWORD_RESET_SECRET`, `SERVICE_TOKEN`, `FIELD_ENCRYPTION_KEY`, `USAGE_HASH_SECRET`, `TELEMETRY_TOKEN_SECRET`
- Shared TLS: `GRPC_ALLOW_INSECURE`, `GRPC_TLS_CA_PATH`, `GRPC_TLS_CERT_PATH`, `GRPC_TLS_KEY_PATH`
- Per-client TLS authority overrides: `<SERVICE>_GRPC_TLS_SERVER_NAME`

### Derive instead of editing directly

These are outputs and should not be treated as first-class editable config:

- `DATABASE_URL`
- `KAFKA_BROKERS`
- `COMMODORE_URL`, `QUARTERMASTER_URL`, `PURSER_URL`, `PERISCOPE_QUERY_URL`, `PERISCOPE_INGEST_URL`, `MISTSERVER_URL`, `MISTSERVER_HTTP_URL`, `HELMSMAN_WEBHOOK_URL`
- `COMMODORE_GRPC_ADDR`, `QUARTERMASTER_GRPC_ADDR`, `PURSER_GRPC_ADDR`, `PERISCOPE_GRPC_ADDR`, `SIGNALMAN_GRPC_ADDR`, `DECKHAND_GRPC_ADDR`, `SKIPPER_GRPC_ADDR`
- `GATEWAY_MCP_URLS`, `GATEWAY_MCP_URL` (derived from Bridge mesh hosts for Skipper)
- `FOGHORN_CONTROL_ADDR`, `FOGHORN_PUBLIC_HTTP_BIND_ADDR`, `FOGHORN_INTERNAL_HTTP_PORT` (default `18027`), `FOGHORN_INTERNAL_HTTP_BIND_ADDR`, `FOGHORN_INTERNAL_GRPC_BIND_ADDR`, `FOGHORN_EXTERNAL_GRPC_BIND_ADDR`, `FOGHORN_EXTERNAL_GRPC_PORT`, `FOGHORN_RELAY_ADVERTISE_ADDR`
- `HELMSMAN_BIND_ADDR`, `HELMSMAN_MANAGEMENT_BIND_ADDR`, `HELMSMAN_MANAGEMENT_PORT` (default `18017`)
- All `VITE_*` (derived by configgen from canonical public URLs)
- `AUTH_PUBLIC_URL` (derived from `GATEWAY_PUBLIC_URL + /auth`)

> Foghorn's HTTP balancer base (the value MistServer's `balance:<base>` source consumes on every edge) is not an environment variable on Helmsman. Foghorn derives it from the edge node's resolved cluster as `https://foghorn.<cluster>.<root-domain>` and ships it to every connected edge over the gRPC `ConfigSeed` stream. Navigator backs that name with the `cluster:<cluster>` TLS bundle, which covers both `<cluster>.<root-domain>` and `*.<cluster>.<root-domain>` so Bunny's cluster apex, `foghorn.<cluster>`, per-service records, and per-node edge records share one refreshed certificate source. `FOGHORN_PUBLIC_BASE` / `FOGHORN_HOST` are fallback escape hatches for deployments without managed cluster DNS.

### Canonical environment selectors

Use `BUILD_ENV` as the repo-wide selector. Keep `NODE_ENV` only where frontend tooling requires it.

### Kafka override rule

Keep service defaults in code and only override generic `KAFKA_CLIENT_ID` or `KAFKA_GROUP_ID` when a deployment actually needs it.

### Keep covered in examples/docs because code really reads them

These are real operator-owned inputs that should stay covered by env examples and operator docs:

- `GRPC_ALLOW_INSECURE`
- `GRPC_TLS_CA_PATH`
- `GRPC_TLS_CERT_PATH`
- `GRPC_TLS_KEY_PATH`
- `<SERVICE>_GRPC_TLS_SERVER_NAME`
- `GRPC_TLS_PKI_DIR`
- `ACME_ENV`
- `CERT_ISSUANCE_TOKEN`
- `BUNNY_API_KEY`
- `NAVIGATOR_GOOGLE_TRUST_EAB_KID`
- `NAVIGATOR_GOOGLE_TRUST_EAB_HMAC_KEY`
- `FEDERATION_ENABLED`
- `FEDERATION_ALLOW_INSECURE_DEV` (isolated development only)
- `RERANKER_API_URL`
- `TURNSTILE_FAIL_OPEN`
- `QUARTERMASTER_GRPC_TLS_SERVER_NAME`
- `NAVIGATOR_GRPC_TLS_SERVER_NAME`
- Crypto settlement/custody inputs documented in the operator guide, including
  `X402_FACILITATOR_*`, optional `CDP_API_KEY_*`, `CRYPTO_SCAN_START_BLOCK_*`,
  `CRYPTO_TREASURY_*`, and `CRYPTO_SWEEP_RELAYER_PRIVATE_KEY_*`. Canonical env
  inputs and Purser's Compose environment must be updated together when this
  set changes. Purser always uses consensus `finalized` heads for
  credit-releasing operations; this is not operator-configurable.

### Naming collisions already cleaned up

ClickHouse naming used to be a confusing area, but the current configgen surface keeps the editable and derived forms split:

- `CLICKHOUSE_HOST` remains the canonical host-only input.
- `CLICKHOUSE_ADDR` is the derived native ClickHouse `host:port` runtime address.
- `CLICKHOUSE_PORT` is the derived HTTP port alias from `CLICKHOUSE_HTTP_PORT`.
- Navigator endpoints are now split cleanly between `NAVIGATOR_GRPC_ADDR` and `NAVIGATOR_HTTP_URL`

## Practical Rule

When adding a new variable, decide this first:

1. Is it canonical operator input?
2. Can it be derived from existing canonical input?
3. Is it only a service-local override?

If the answer is "derived", it should not become another hand-maintained env key.

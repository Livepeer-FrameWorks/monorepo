# Bootstrap Desired State

> The contract between the operator CLI, GitOps, and the per-service `<service> bootstrap`
> commands. This document defines the YAML schema, ownership boundaries, layered
> composition, and drift policy. The file carries no schema-version field — each
> service decodes with `KnownFields(true)`, so unknown or typo'd fields fail parse.
> If a real on-the-wire break shows up, we add a `schema:` integer at that
> moment, not preemptively.

## Most clusters do not need a handwritten overlay

The CLI derives almost all bootstrap state from the cluster manifest (`cluster.yaml` plus
SOPS-encrypted host inventory). For a standard platform deployment, the rendered
desired-state file is produced from the manifest alone: `cluster provision --ready` works
end-to-end with zero overlay file.

Operators only author an overlay when they need to express something the manifest cannot
safely infer: extra customer tenants, custom billing tiers, declarative operator users,
private-cluster overrides. The overlay lives in `gitops/clusters/<id>/bootstrap.yaml` by
default and is referenced from the manifest via `bootstrap_overlay: <relative path>`.

---

## Layered composition

Five explicit layers compose the bootstrap state. Each has a different owner, lifecycle,
and visibility:

| #   | Layer                          | Owner                                 | Lifecycle                                 |
| --- | ------------------------------ | ------------------------------------- | ----------------------------------------- |
| 1   | Schema migrations              | per-service migrations                | per-deploy, run before service boots      |
| 2   | Static service catalog         | service binary (embedded)             | versioned with the binary                 |
| 3   | Manifest-derived desired state | CLI renderer                          | per-cluster, regenerated each provision   |
| 4   | GitOps overlay                 | hand-authored, committed in `gitops/` | per-cluster, optional                     |
| 5   | Secret refs                    | SOPS / env / CLI flag                 | resolved at execute time, never committed |

The CLI renders **layers 3 + 4 + resolved 5** into the rendered desired-state file.
Layer 2 is **not** in the file — service binaries ship their own catalog (e.g. Purser's
billing tiers) and merge it at execute time. Layer 1 is a separate concern (DB
migrations) that runs before the service boots.

### Merge precedence

`derived ← overlay ← secret refs resolved`

- The overlay can **add** items the manifest did not express (e.g. a new customer tenant).
- The overlay can **override** mutable fields on items the manifest did express, but only
  when the field is marked overridable (see drift policy per resource below). Stable keys
  cannot be overridden silently — fail loud unless the overlay carries an explicit
  `override: true` marker.
- Secret refs in either layer are resolved into concrete values at render time. The
  rendered file may contain plaintext secrets; it is mode `0600`, not committed, and
  removed after use.

---

## Domain ownership

Each resource type in the rendered file is owned by exactly one service. Bootstrap
subcommands write only to their owner's tables; cross-service writes happen through the
owning service's gRPC API.

| Domain                             | Owner         | Examples                                                                                                                                                                             |
| ---------------------------------- | ------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------ |
| User identity                      | Commodore     | user row, password hash, role/permissions, wallet identity, verification state                                                                                                       |
| Tenant identity & cluster topology | Quartermaster | tenant row, attribution, primary/official cluster ids, infrastructure clusters, nodes, mesh config, service registry, ingress sites, TLS bundles, `tenant_cluster_access`            |
| Billing & entitlement              | Purser        | billing tiers (catalog), `cluster_pricing`, subscriptions, prepaid/postpaid balances. Purser turns subscription state into Quartermaster `tenant_cluster_access` rows by calling QM. |

**Cluster owner is a tenant, not a user.** A "customer account" therefore comprises three
things across three services: a Quartermaster tenant shell, a Purser billing+access
reconcile (after clusters and pricing exist), and a Commodore user attached to the tenant.

---

## Top-level shape

```yaml
quartermaster:
  system_tenant: { ... }
  tenants: [ ... ]                 # customer-tenant shells from overlay
  clusters: [ ... ]                # owner_tenant ref resolves against tenants above
  nodes: [ ... ]
  mesh: { ... }
  ingress:
    tls_bundles: [ ... ]
    sites: [ ... ]
  service_registry: [ ... ]
  system_tenant_cluster_access:    # post-cluster reconcile (see plan §System-tenant timing)
    default_clusters:        true
    platform_official_clusters: true

purser:
  cluster_pricing: [ ... ]         # one row per cluster that declares pricing
  customer_billing: [ ... ]        # one entry per customer tenant from overlay

commodore:
  # users live under accounts (see below); commodore: section is reserved for future
  # commodore-owned bootstrap state that doesn't fit the account model.

accounts:
  - kind: system_operator
    tenant: { ref: quartermaster.system_tenant }
    users: [ ... ]
    billing: none
  - kind: customer
    tenant: { ref: quartermaster.tenants[<alias>] }
    users: [ ... ]
    billing: { ... }
```

The `accounts:` array is the cross-service coordination surface. Each entry pins a
tenant + users + billing relationship. The CLI renderer validates intra-file
references, then the service-specific bootstrap commands reconcile their owned
sections; there is not currently a top-level `frameworks bootstrap validate`
command.

---

## Section: `quartermaster`

### `system_tenant`

```yaml
system_tenant:
  alias: frameworks # stable key — the canonical alias; reserved
  name: FrameWorks
  deployment_tier: global
  primary_color: "#6366f1"
  secondary_color: "#f59e0b"
```

The rendered file does **not** carry the Quartermaster tenant UUID. Service bootstrap
subcommands resolve aliases to UUIDs via QM's persisted alias mapping at apply time,
so re-runs always find the same tenant by alias.

| Field                             | Stable | Source                                                      |
| --------------------------------- | ------ | ----------------------------------------------------------- |
| `alias`                           | yes    | Hardcoded `frameworks` for the system tenant. Drift = fail. |
| `name`, `deployment_tier`, colors | no     | Manifest defaults; overlay can override.                    |

### `tenants` (customer/private cluster owners)

```yaml
tenants:
  - alias: northwind # stable key — operator-chosen, hand-readable
    name: Northwind Traders
    deployment_tier: regional
```

Stable key: `alias`. The `frameworks` alias is reserved for the system tenant and
cannot be used here. Drift = fail. All other fields update-on-drift.

**Consumer obligation**: `quartermaster bootstrap` must persist the alias → UUID
mapping in QM-owned storage (e.g. a `quartermaster.bootstrap_tenant_aliases` table
keyed by alias) so re-runs find the same tenant. Name-based fallback is rejected
because names are mutable.

Customer tenants only appear when the overlay declares them. Most provisions have no
`tenants:` entries (the system tenant is separate above).

### `clusters`

```yaml
clusters:
  - id: media-central-primary
    name: Media Central Primary
    type: central # central | edge
    region: eu-central
    owner_tenant: { ref: quartermaster.system_tenant } # references system_tenant.id or tenants[*].id
    is_default: true
    is_platform_official: true
    base_url: https://frameworks.network
    mesh:
      cidr: 10.99.0.0/16
      listen_port: 51820
```

| Field                               | Stable | Notes                                                                                                        |
| ----------------------------------- | ------ | ------------------------------------------------------------------------------------------------------------ |
| `id`                                | yes    | Drift = fail.                                                                                                |
| `mesh.cidr` (when cluster exists)   | yes    | Drift = fail; mesh CIDR change is a re-provision, not an update.                                             |
| `name`, `region`, `base_url`, flags | no     | Update-on-drift.                                                                                             |
| `mesh.listen_port`                  | no     | Update-on-drift; verify nodes still reach each other.                                                        |
| `owner_tenant`                      | yes    | TenantRef. Drift = fail. Reassigning cluster ownership is a deliberate operation, not a bootstrap reconcile. |

Default-cluster uniqueness is enforced **inside the same transaction** as the
Quartermaster bootstrap cluster upsert.

### `nodes`

```yaml
nodes:
  - id: core-eu-1
    cluster_id: core-central-primary
    type: core # core | edge
    external_ip: 203.0.113.10
    wireguard:
      ip: 10.99.0.1
      public_key: <pubkey>
      port: 51820
```

Stable: `id`, `cluster_id`, `external_ip`, `wireguard.ip`. Drift on any of these = fail
(the existing `CreateNode` handler enforces this; the reconciler keeps that semantic).

### `mesh`

```yaml
mesh:
  cidr: 10.99.0.0/16
  listen_port: 51820
  manage_hosts_file: true
```

Per-cluster mesh state. Stable on `cidr`. Listen port update-on-drift.

### `ingress.tls_bundles`

```yaml
ingress:
  tls_bundles:
    - id: wildcard-frameworks-network
      cluster_id: media-central-primary
      domains: ["*.frameworks.network", "frameworks.network"]
      issuer: lets-encrypt
      email: info@frameworks.network
```

Stable: `id`. All other fields update-on-drift.

Auto-derived bundles are rendered with `issuer: navigator` and a required
contact email resolved from shared env in this order: `TLS_BUNDLE_EMAIL`,
`ACME_EMAIL`. Explicit `tls_bundles` entries carry their own `email`; a missing
email is invalid.

### `ingress.sites`

```yaml
ingress:
  sites:
    - id: bridge-media-central-primary
      cluster_id: media-central-primary
      node_id: regional-eu-1
      domains: ["bridge.frameworks.network"]
      tls_bundle_id: wildcard-frameworks-network
      kind: http
      upstream: { host: 10.99.0.5, port: 18008 }
```

Stable: `id`. Domain list update-on-drift. Upstream change update-on-drift but logged.

### `service_registry`

```yaml
service_registry:
  - service_name: chandler
    type: chandler
    protocol: http # http | grpc | tcp — sourced from servicedefs
    cluster_id: media-central-primary
    node_id: regional-eu-1
    port: 18020
    health_endpoint: /health
    metadata: { region: eu-central }
```

Stable: `(service_name, node_id)` pair. Update-on-drift for port / health / metadata.

Multi-host services emit one row per host. Cluster id is resolved per host
(`manifest.HostCluster(node)`), so a service deployed across hosts in different
clusters produces one cluster-correct row per host.

`metadata` is rendered authoritatively from the manifest. The bootstrap reconciler
writes it as-is and must not enrich it from on-host state — if a service needs
values that only exist at apply time, model them as SecretRef-backed fields in the
schema, not as silent runtime reads.

**livepeer-gateway** service-registry metadata contains only per-instance
invariants such as `public_port`, `public_scheme`, and `wallet_address`.
Quartermaster synthesizes `public_host` inside `DiscoverServices` from the
requested logical media-cluster assignment, so one physical gateway pool can
serve multiple media clusters without storing one static host on the instance.
Purser's deposit monitor reads `wallet_address` to credit tenant deposits.

`public_port` is the manifest service port.

Admin / CLI port endpoints (default `:7935`, container-local in Docker mode) are
intentionally **not** modeled here — operator transport (SSH tunnel, ansible-local
exec, `docker exec`) is the right path for admin operations, not public service
discovery.

**`wallet_address` is required for every `livepeer-gateway` service-registry
entry.** Purser's deposit monitor
(`api_billing/internal/handlers/livepeer_deposit.go`'s
`LivepeerDepositMonitor.discoverGatewayAddresses`) skips any gateway whose
registry metadata lacks it. The renderer resolves `wallet_address` in this
order:

1. The livepeer-gateway service's `config.eth_acct_addr` /
   `config.LIVEPEER_ETH_ACCT_ADDR` in `cluster.yaml`.
2. The operator's shared env (typically `gitops/config/<profile>.env`),
   keyed `LIVEPEER_ETH_ACCT_ADDR` or `eth_acct_addr`.

Validation rejects any livepeer-gateway entry without a resolved
`wallet_address`, so a misconfigured manifest fails at render time rather
than silently dropping deposit monitoring at runtime.

### `system_tenant_cluster_access`

```yaml
system_tenant_cluster_access:
  default_clusters: true # subscribe system tenant to every is_default cluster
  platform_official_clusters: true # and every is_platform_official cluster
```

Reconciled **after** clusters exist (per plan §System-tenant timing). Idempotent upsert
against `tenant_cluster_access`. No customer access is reconciled here — Purser owns
that.

---

## Section: `purser`

### `cluster_pricing`

```yaml
cluster_pricing:
  - cluster_id: media-central-primary
    pricing_model: tiered
    required_tier_level: 2
    allow_free_tier: false
    base_price: "29.00"
    currency: USD
    metered_rates: { ... }
    default_quotas: { ... }
```

Stable: `cluster_id`. All else update-on-drift.

Source: derived from `cluster.yaml`'s `clusters[*].pricing` (existing
`ClusterPricingConfig` at `cli/pkg/inventory/types.go:80`). Overlay can add or override.

**Validation invariant** (checked by `purser bootstrap validate`): every cluster with
`is_platform_official: true` in Quartermaster must have a `cluster_pricing` row here.
Failure = non-zero exit, structured error on stderr.

### `customer_billing`

```yaml
customer_billing:
  - tenant: { ref: quartermaster.tenants[northwind] }
    model: prepaid # prepaid | postpaid
    tier: developer # ref into Purser catalog (built-in or overlay-added)
    cluster_access: derived # derived from tier eligibility + is_platform_official
```

Only `customer`-kind accounts produce entries here. `system_operator` accounts have
`billing: none` and never appear in this section.

Stable: `tenant.ref`. Update-on-drift for `model` and `tier`.

When this reconciles, Purser resolves the tenant alias to a UUID, writes the
tenant's billing/subscription state, and emits post-commit access grants for the
tier's eligible platform-official clusters. Those grants call Quartermaster's
service-token `BootstrapClusterAccess` primitive.

---

## Section: `accounts`

The account section is the cross-service coordination surface. Each entry references a
tenant, attaches users to it, and declares billing for that tenant.

### `kind: system_operator`

```yaml
accounts:
  - kind: system_operator
    tenant:
      ref: quartermaster.system_tenant
    users:
      - email: ops@example.com
        role: owner
        first_name: Platform
        last_name: Ops
        password_ref: { sops: gitops/secrets/ops.env, key: OPS_PASSWORD }
    billing: none
```

Operator accounts skip Purser by design. Commodore reconciles the user; tenant
verification goes through Quartermaster (existing `CreateUserInTenant` flow at
`api_control/internal/grpc/server.go:6879`, with auth check stripped in the bootstrap
reconciler).

### `kind: customer`

```yaml
accounts:
  - kind: customer
    tenant:
      ref: quartermaster.tenants[northwind]
    users:
      - email: admin@northwind.example
        role: owner
        password_ref: { sops: ..., key: NORTHWIND_OWNER_PASSWORD }
    billing:
      model: prepaid
      tier: developer
      cluster_access: derived
```

Customer-kind seeding ships when needed. The ordering is: tenant shell (QM step 2) →
clusters (QM step 2) → cluster_pricing (Purser step 3) → customer_billing (Purser step 3,
calls QM `BootstrapClusterAccess` for eligible platform clusters) → user (Commodore
step 4).

### User reconciliation (idempotent)

Commodore reconciler semantics for any user under `accounts[*].users`:

- Lookup by email (global — `commodore.users.email` has no per-tenant uniqueness).
- If found in the same tenant with matching `role`, `first_name`, `last_name`,
  `is_active=true`, `verified=true` → no-op.
- If found in the same tenant with mismatched mutable fields → update.
- If found in a **different** tenant → fail loud. Bootstrap does not silently move users
  between tenants.
- Password is rewritten only when the desired-state file declares
  `reset_credentials: true` on the user **and** the bootstrap subcommand is invoked with
  `--reset-credentials`.

---

## Secret references

Any field carrying a secret value uses a reference, never a literal in committed files:

```yaml
password_ref:
  sops: gitops/secrets/ops.env # SOPS-encrypted file (env or yaml format)
  key: OPS_PASSWORD # field within the decrypted file
```

Other supported forms:

```yaml
password_ref: { env: OPS_PASSWORD }              # environment variable
password_ref: { file: /run/secrets/ops_password } # local file (mode 0400 expected)
password_ref: { flag: bootstrap-admin-password } # CLI flag at render time
```

The CLI resolves all `*_ref` fields at render time. The rendered file (which contains
plaintext) is written `0600` to a temp path, uploaded via Ansible, consumed by the
service binary, and removed.

---

## Apply-time invariants

When `<service> bootstrap` runs against a rendered file, it enforces:

1. **Section presence is optional but typed.** A bootstrap file with no `purser:` section
   is valid for `purser bootstrap` (no-op). A bootstrap file with `purser: null` is also
   valid. A bootstrap file with `purser: {}` is also valid (empty cluster_pricing).
2. **Reference integrity:** every `clusters[*].owner_tenant.ref`, every
   `accounts[*].tenant.ref`, every `customer_billing[*].tenant.ref` resolves to a
   tenant present in the same file.
3. **Drift on stable keys = non-zero exit** with the exact field path in the structured
   error.

---

## Validation surface

The rendered file is exercised through:

- `<service> bootstrap --check --file <path>` — schema parse + reference integrity, no
  DB connection. Always available, always cheap.
- `<service> bootstrap --dry-run --file <path>` — full reconcile inside a transaction
  that rolls back. Prints the change set.
- `<service> bootstrap validate` — cross-service post-state invariants. No file
  input — the subcommand queries the service's own DB and sibling services' gRPC.
  Available only for services that own a real cross-service invariant. Today Purser
  exposes this and checks that every platform-official cluster reported by
  Quartermaster has a Purser pricing row. Services without such an invariant do not
  expose a `validate` subcommand.

---

## Examples

### Minimal platform — derived only, no overlay

A standard `cluster provision --ready` against a manifest with one central cluster and an
optional `--bootstrap-admin-email` flag produces a rendered file equivalent to:

```yaml
quartermaster:
  system_tenant:
    alias: frameworks
    name: FrameWorks
    deployment_tier: global
  clusters:
    - id: core-central-primary
      name: Core Central Primary
      type: central
      owner_tenant: { ref: quartermaster.system_tenant }
      is_default: true
      is_platform_official: true
      base_url: https://frameworks.network
      mesh: { cidr: 10.99.0.0/16, listen_port: 51820 }
  nodes:
    - id: core-eu-1
      cluster_id: core-central-primary
      type: core
      external_ip: 203.0.113.10
      wireguard: { ip: 10.99.0.1, public_key: …, port: 51820 }
  ingress: { tls_bundles: […], sites: […] }
  service_registry: […]
  system_tenant_cluster_access:
    default_clusters: true
    platform_official_clusters: true

purser:
  cluster_pricing:
    - cluster_id: core-central-primary
      pricing_model: tiered
      required_tier_level: 2

accounts:
  - kind: system_operator
    tenant: { ref: quartermaster.system_tenant }
    users:
      - email: ops@example.com
        role: owner
        password_ref: { flag: bootstrap-admin-password }
    billing: none
```

### With overlay — extra customer tenant + private cluster

```yaml
# gitops/clusters/media-central-primary/bootstrap.yaml
quartermaster:
  tenants:
    - alias: northwind
      name: Northwind Traders
      deployment_tier: regional

  clusters:
    - id: northwind-private-eu
      name: Northwind Private EU
      type: edge
      owner_tenant: { ref: quartermaster.tenants[northwind] }
      is_default: false
      is_platform_official: false
      mesh: { cidr: 10.99.16.0/20, listen_port: 51820 }

purser:
  cluster_pricing:
    - cluster_id: northwind-private-eu
      pricing_model: flat
      base_price: "499.00"
      currency: USD

accounts:
  - kind: customer
    tenant: { ref: quartermaster.tenants[northwind] }
    users:
      - email: admin@northwind.example
        role: owner
        password_ref: { sops: gitops/secrets/northwind.env, key: NORTHWIND_OWNER_PASSWORD }
    billing:
      model: prepaid
      tier: developer
      cluster_access: derived
```

The CLI merges the overlay onto the manifest-derived state. The final rendered file is
the union, with overlay-supplied tenants/clusters/pricing/accounts added and any
overlay-marked overrides applied to derived items.

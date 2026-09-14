# Node Enrollment - Provenance Model and GitOps Adoption

Every row in `quartermaster.infrastructure_nodes` carries an `enrollment_origin`
that records how the node joined and — critically — who owns its WireGuard private
key. This governs what the bootstrap reconciler may mutate, how audit treats the
node, and the adoption path that moves runtime-joined nodes under GitOps authority.

## The three origins

| Origin             | How the row was created                                             | WG private key custody                                                                        |
| ------------------ | ------------------------------------------------------------------- | --------------------------------------------------------------------------------------------- |
| `gitops_seed`      | Declared in `cluster.yaml`; inserted by the QM bootstrap reconciler | SOPS (`hosts.enc.yaml`); Ansible renders `/etc/privateer/wg.key`. Cold-boot capable.          |
| `runtime_enrolled` | Joined via a bootstrap token after the cluster was alive            | Generated and held on the node; the control plane only ever sees the public key               |
| `adopted_local`    | Runtime-enrolled node whose _public_ identity is now in GitOps      | Still on the node; Ansible preserves the on-disk key (`wireguard_private_key_managed: false`) |

`quartermaster.service_cluster_assignments.source` mirrors the same provenance idea
(`gitops_seed` | `runtime` | `adopted_local`) for logical service-to-cluster rows:
ordinary runtime upserts preserve the existing source; only explicit adopt/unmanage
operations flip it.

## Enrollment paths

**GitOps seed** — `cluster provision` renders the node into the bootstrap
desired-state file; the QM bootstrap reconciler inserts it with
`enrollment_origin = 'gitops_seed'` (`api_tenants/internal/bootstrap/nodes.go`).
Stable keys (`node_id`, `external_ip`, `wireguard.ip`, `wireguard.public_key`) fail
loud on drift; heartbeats, runtime status, and mesh-revision columns are owned by the
running node and never touched by bootstrap. Bootstrap may move a node between
clusters **only** when the row is `gitops_seed` or `adopted_local` — it refuses to
move `runtime_enrolled` rows it does not own.

**Runtime enrollment** — a node joins with a bootstrap token. Privateer's enroll flow
generates the WG keypair locally, persists a `pendingEnrollment` (0600) before the
RPC so a crash between server commit and local commit is replay-safe (the server's
replay branch, keyed on token + node_id + public_key, returns the already-assigned
identity without consuming a fresh token), and Quartermaster inserts the row with
`enrollment_origin = 'runtime_enrolled'`. The private key never leaves the node.
Privateer's managed sync loop also re-registers from local identity if QM loses the
row (`CreateNode` on `NotFound`) — the re-created row is again `runtime_enrolled`.

## Adoption: `mesh reconcile --write-gitops`

`frameworks mesh reconcile` implements the **Adopt-Without-Import** model: only the
node's public identity is written back to GitOps; private keys are never fetched from
the node.

1. Lists QM nodes with `enrollment_origin = runtime_enrolled` inside the manifest's
   clusters, partitioned into _pending_ (not in the manifest yet) and _in-progress_
   (manifest written by a prior run that failed before the origin flip — this makes
   reconcile idempotent across partial failures).
2. Without `--write-gitops` it only prints the plan. With it, pending hosts are
   written into `cluster.yaml` + `hosts.enc.yaml` using the same staged-then-committed
   flow as `mesh wg generate` (so the two files can't desync), with
   `wireguard_private_key_managed: false` telling Ansible to preserve the on-disk key.
3. Origins flip via the idempotent `SetNodeEnrollmentOrigin` RPC with
   `expected_current = "runtime_enrolled"` — a compare-and-set that refuses stale
   flips. Result: `runtime_enrolled → adopted_local`.

## Promotion: taking key custody

Full GitOps key authority (`adopted_local → gitops_seed`) is a deliberate two-step:

1. `frameworks mesh wg rotate <host>` writes a new SOPS-managed key and clears the
   preserve-key markers; `cluster provision` renders the new `/etc/privateer/wg.key`
   and Privateer SyncMesh's the new public key back to QM.
2. `frameworks mesh wg promote <host>` verifies QM's recorded public key matches the
   manifest (i.e. the node actually adopted the rotated key) and only then flips the
   origin. Promote is deliberately separate from rotate: flipping at rotate time
   would claim GitOps authority before the running node converged. If QM's key
   hasn't converged, promote fails with a retry-after-SyncMesh message.

## Legacy keyless fingerprint recovery

Fingerprint lookup is identification, not key enrollment. A keyless or
malformed stored key always returns `FailedPrecondition` with
`enrollment_token_required`; there is no compatibility mode that treats a
request-supplied key as authority.

The `quartermaster_node_identity_keys_v0_3_0` data migration is a read-only
census and release gate for active `gitops_seed` and `adopted_local` nodes,
which are the nodes the platform operator can remediate. A `runtime_enrolled`
self-hosted node does not block the platform release; the strict runtime
identity check still applies when that node connects, so it must re-enroll
before it can rejoin. Before the strict binary is installed, operators run
the equivalent `SELECT` from the self-hosted edge rollout runbook directly
against the existing Postgres service; that pre-deploy census does not depend
on the replacement Quartermaster binary or an adoption marker. Inventory
affected node IDs, schedule their brief disconnect, and block postdeploy until
each one has been recovered. Operators recover an edge node by issuing a fresh
`edge_node` token for its existing tenant and cluster, preserving its hostname
and stable fingerprint, and reconnecting once with
`HELMSMAN_ROTATE_NODE_IDENTITY=true`. `BootstrapEdgeNode` locks and validates
the token and existing binding, rotates the key, consumes token usage, and
commits those changes atomically. When Foghorn returns the first accepted
`ConfigSeed`, Helmsman persists a non-secret enrollment receipt and stops
sending the consumed token on reconnects. Rotation completion is persisted
before cleanup is requested. Native installs scrub the managed enrollment file
and `HELMSMAN_ROTATE_NODE_IDENTITY` directly. The edge container instead asks a
narrow root helper to scrub the root-owned bind mounts, so the non-root
Helmsman process never needs host-file ownership changes. Cleanup retries until
both value-only rewrites and the request removal are durable; neither rewrite
truncates its surrounding environment file. The helper has no feature knob: it
runs in every managed edge container, accepts only the fixed `/data/state`,
`/run/frameworks/.edge-enroll.env`, and `/run/frameworks/.edge.env` paths,
walks enrollment state through no-follow directory descriptors, refuses
symlinks and non-regular targets, verifies that the request and node receipt
belong to Helmsman's runtime UID, and rejects credential files owned by that
UID. Repeated attempts for the same accepted enrollment retain the
request's original age. A later accepted rotation atomically replaces an
obsolete request so stale native or interrupted cleanup state cannot block the
new credential generation. Helmsman exports pending/age/outcome metrics plus a distinct
observation-error gauge. Platform alerts distinguish a readable request that is
not draining from cleanup state that cannot be safely read or trusted. Once a
pending request has been observed, a disappearing state mount is an observation
failure and retains the last known pending age. The managed container also treats
a missing state directory as an observation failure after a process restart in
both container and native modes, because completion cannot be proven without
the configured state mount. A direct scrub
failure is returned to the control loop as expected pending cleanup and logged at
warning level with its underlying filesystem/configuration cause after the
durable retry request is created. Supplying
an enrollment credential without both managed credential-file paths creates
visible, retryable cleanup work instead of reporting a successful no-op. The
request is a cleanup trigger, not an authentication
proof: a compromised Helmsman can already withhold registration and sees the
same token in its process environment. The helper therefore grants it no wider
root capability—its only possible mutation is blanking those two fixed fields
after the node-specific local acceptance receipt exists. A fresh explicit
rotation still uses its fresh token. Operators should verify the managed token
file is empty after a successful reconnect; unmanaged deployments must remove
the bearer secret themselves.
During a Quartermaster outage, Foghorn may restore only a previously persisted,
unexpired admission whose stable fingerprint digest and Ed25519 public key both
match; a stale configured enrollment token does not disable that local recovery.
An ambiguous fingerprint blocks the current authority lookup but is not a
revocation signal: Foghorn preserves the existing local pin without using it
while Quartermaster is reachable, so cloned-image ambiguity cannot erase an
otherwise valid outage fallback.
The release gate clears once the census reports zero.

Never repair this condition with a direct database update or by deleting the
fingerprint. A replacement host whose stable fingerprint does not match needs a
new enrollment identity; knowing the old node ID or observed IP is insufficient.

`frameworks mesh wg audit` cross-checks manifest identity, QM state, and each
agent's reported `applied_mesh_revision` (see `privateer-mesh.md`).

## Key Files

- `pkg/database/sql/schema/quartermaster.sql` - `enrollment_origin` column + provenance comments
- `api_tenants/internal/bootstrap/nodes.go` - GitOps-side reconciler and ownership rules
- `api_tenants/internal/grpc/server.go` - runtime `CreateNode` (token path), `SetNodeEnrollmentOrigin`
- `api_mesh/cmd/privateer/enroll.go` - replay-safe runtime enrollment substrate
- `cli/cmd/mesh_reconcile.go` - adoption (`--write-gitops`)
- `cli/cmd/mesh_wg_promote.go` - convergence-checked promotion
- `cli/internal/mesh/manifest.go` - staged GitOps file writes

## Gotchas

- `adopted_local` looks GitOps-managed in the manifest but the key is still
  node-local: losing the host loses the key, and a re-provision cannot cold-boot the
  mesh identity until `wg rotate` + `promote` complete the promotion.
- Bootstrap treats `runtime_enrolled` rows as read-only for placement: a
  `cluster_id` mismatch against the manifest is an error, not a move.
- `SERVICE_TOKEN` delivery differs by origin: Ansible for seed nodes, `mesh join`
  for runtime-enrolled ones — enrollment state on disk carries no bearer
  credentials.
- A legacy keyless edge is offline after the strict Quartermaster rollout until
  token-authorized recovery completes. Plan that interruption from the census;
  the release gate prevents silently carrying a keyless active row forward.

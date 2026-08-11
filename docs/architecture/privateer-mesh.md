# Privateer Mesh - Three-Layer WireGuard Apply Model

Privateer (`api_mesh`) is the per-node mesh agent: it keeps `wg0` configured, serves
`.internal` DNS, and syncs TLS material onto disk. This document covers the mesh
substrate itself — how a node decides _which_ WireGuard configuration to apply, and
why a reboot or a control-plane outage never leaves the mesh down. Certificate
distribution is covered in `tls.md`; enrollment provenance in `node-enrollment.md`.

## Architecture

```
                 GitOps (Ansible privateer role)
                 ├── /etc/privateer/wg.key            (identity: private key)
                 ├── MESH_WIREGUARD_IP + MESH_LISTEN_PORT (identity: address)
                 └── static-peers.json                (seed layer, versioned)
                                │
                                ▼
  ┌───────────────────────── Privateer agent ─────────────────────────┐
  │  startup:  last_known_mesh.json ──► wg0     (layer=last_known)    │
  │            else static-peers.json ─► wg0    (layer=seed)          │
  │  runtime:  SyncMesh loop (30s) ────► wg0    (layer=managed)       │
  │            └─ persists merged view ─► /var/lib/privateer/         │
  │                                        last_known_mesh.json       │
  └───────────────────────────────┬───────────────────────────────────┘
                                  │ SyncMesh (public key, listen port,
                                  │ applied_mesh_revision, resource snapshot)
                                  ▼
                          Quartermaster (mesh authority)
```

## The three layers

`privateer_layer_applied{layer}` reports which layer's config is currently on
`wg0`:

| Layer        | Source                                                       | When applied                                                                             |
| ------------ | ------------------------------------------------------------ | ---------------------------------------------------------------------------------------- |
| `managed`    | Live `SyncMesh` response from Quartermaster                  | Every successful sync tick (default every 30s)                                           |
| `last_known` | `/var/lib/privateer/last_known_mesh.json` (`source=dynamic`) | At startup, before the first sync — cache of the most recent successful managed apply    |
| `seed`       | Ansible-rendered `static-peers.json`                         | At startup when no usable last-known snapshot exists, or when the snapshot is a seed one |

Startup precedence (`Agent.applyStartupMesh`):

1. A last-known snapshot with `source: dynamic` always applies — Quartermaster's most
   recent view is authoritative, merged over the current seed file.
2. A last-known snapshot with `source: seed` applies only when its version matches the
   on-disk seed file's version (GitOps unchanged during downtime). The seed file
   carries an embedded version, or a stable content hash is derived.
3. Otherwise the current `static-peers.json` is rendered fresh and persisted as a new
   `source: seed` snapshot.

The managed loop then takes over: each successful `SyncMesh` applies the dynamic peer
set merged over the seed peers (seed entries act as a floor; dynamic entries with the
same public key win), updates `.internal` DNS records, and atomically rewrites
`last_known_mesh.json`. The result is that the mesh comes up on a fresh boot or while
Quartermaster is down, and converges to managed state as soon as the control plane is
reachable.

## Identity is never read from the snapshot

Self identity — the WireGuard private key (`MESH_PRIVATE_KEY_FILE`), the mesh address
(`MESH_WIREGUARD_IP`), and the listen port — is **always** read from the GitOps
identity layer on disk. The persisted snapshot stores copies of `wireguard_ip` and
`listen_port` for diagnostics only; `lastKnownToWireGuard` ignores them when
reconstructing the device config.

This is the invariant that makes GitOps key rotation converge: after a rotation, a
rebooted agent cannot resurrect the old key or address from a stale cached snapshot.
The managed path enforces the same thing from the other direction —
`validateManagedSelfIdentity` rejects any `SyncMesh` response whose `wireguard_ip` or
listen port conflicts with the local GitOps identity, and the sync fails rather than
applying a config under a conflicting identity.

## Apply pipeline and failure semantics

Every layer goes through the same pipeline: parse peers at the boundary
(`parsePeerStrings`), build the typed config, `wireguard.ValidateForApply` (policy),
`wgManager.Apply`, then DNS record update. On the managed path a DNS update failure
rolls back the WireGuard config so the device never diverges from DNS.

Failures are counted per `{layer, reason}` in `privateer_mesh_apply_failures_total`
with a stable reason enum: `private_key`, `invalid_identity`, `invalid_peer`,
`policy`, `configure`, `dns`. Apply latency is
`privateer_mesh_apply_duration_seconds{layer}`; the settled peer count is
`privateer_mesh_peer_count{layer}` (updated only on fully successful applies, so a
rollback never reports peers that are no longer on the device).

If `SyncMesh` returns `NotFound`/`FailedPrecondition`, the agent re-registers its node
from local identity (`CreateNode`) and retries — see `node-enrollment.md` for how the
resulting row's provenance is tracked.

## Revision reporting

Each `SyncMesh` response carries a `mesh_revision`. The agent stores the revision of
its most recent successful managed apply (also persisted into the snapshot) and
reports it back on subsequent `SyncMesh` requests, including the first one after a
restart. `frameworks mesh wg audit` renders the reported revision
(`AppliedMeshRevision`) as a display-only column; its actual comparison is a
WireGuard identity diff plus heartbeat liveness — it does not compare reported
revisions against Quartermaster's current revision.

## Health

- Mesh substrate: unhealthy after >3 consecutive sync failures or >5 minutes without
  a successful sync.
- Internal PKI: a separate health check (`internal_pki`) goes unhealthy after 3
  consecutive certificate-sync failures (see `tls.md`), so PKI degradation is
  distinguishable from mesh degradation.

## Key Files

- `api_mesh/internal/agent/agent.go` - sync loop, startup layering, managed apply, metrics
- `api_mesh/internal/agent/static.go` - seed/last-known schemas, merge and persistence, identity-ignore invariant
- `api_mesh/internal/wireguard/` - typed config, policy validation, device apply
- `api_mesh/cmd/privateer/enroll.go` - runtime enrollment substrate (see `node-enrollment.md`)
- `pkg/database/sql/schema/quartermaster.sql` - mesh authority tables (nodes, revisions)

## Gotchas

- Seed peers are merged into every managed apply as a floor. A peer removed from
  GitOps but still present in `static-peers.json` on the host keeps existing until
  the seed file is re-rendered — removal is a provision concern, not a sync concern.
- `last_known_mesh.json` is written atomically (temp + rename, mode 0640). Deleting
  it is safe; the agent falls back to the seed layer on next start.
- The snapshot's identity fields are diagnostics. Editing them does nothing; the
  on-disk key/IP/port always win.

# RFC: Cross-Cluster Durable Replication v1

## Status

Proposed — NOT implemented. Foundation for [`placement-policy-engine.md`](placement-policy-engine.md),
which depends on this protocol. The current storage-artifact-catalog release ships a truthful
single-provider foundation (local/official durable storage only); this RFC adds ONE remote durable
destination: an explicitly subscribed storage-provider cluster.

## TL;DR

- A node may replicate its tenant's media to a REMOTE subscribed storage-provider cluster, server-selected,
  with provider consent, verified by the destination, and attributed for customer billing + provider
  settlement.
- Depends on cluster-bound service identity (see Impact / Dependencies): the current federation trust root
  (shared `SERVICE_TOKEN` over server-authenticated TLS) is NOT sufficient to root a provider attestation.
- Not the placement engine: exactly one server-chosen destination; no multi-copy, tiers, or policy.

## Current State (single-provider foundation in the current release — do NOT rebuild here)

- `processFreezePermissionRequest`: possession gate + `authorizeStorageReplication`. Storage authority is
  restricted to the tenant's **official** cluster with **active, unexpired** `tenant_cluster_access` (the
  Quartermaster peer query filters `expires_at`); generic serving/subscribed access does NOT authorize
  storage writes; control-cell membership is never authority.
- Freeze mints a SERVER-MINTED attempt id (echoed by the node at completion) and a presigned PUT to an
  attempt-scoped STAGING key; completion HEAD-verifies the staging object (refusing a missing / 0-byte
  object, recording the provider-observed size) and PUBLISHES it — a conditional copy to a FRESH, immutable
  attempt-versioned key (`active_object_key`) followed by an atomic pointer flip in the transaction, so a
  publish never overwrites a served object and a rollback cannot expose uncommitted bytes. The completion
  identity read is scoped to the server-assigned attempt + node.
- Billing (`GetColdStorageUsage`) aggregates only rows THIS provider owns, read from the STABLE per-row
  `durable_backend_local` column captured at write time (the freeze claim, the VOD-upload completion) — NOT
  recomputed from mutable tenant routing at read time. Playback-federation adopted-remote rows are left
  `durable_backend_local = false` and excluded. An owned synced artifact whose type has no billing bucket
  FAILS the snapshot (never a silent under-bill), and `rows.Err()` is checked so a truncated read never
  reads as complete.

## Problem / Motivation

BYOC / self-hosted and marketplace clusters replicating a tenant's media into ANOTHER cluster's durable
storage — and being billed / settled for it — is a core product path. Today anything remote is rejected
(`official_storage_remote` / `remote_not_durable`). This RFC defines the bounded protocol that safely adds
the remote destination.

## Goals

- Explicit `store` / `replicate` entitlement (not generic serving access) with active/unexpired enforcement.
- Provider-side writable-storage capability + per-(provider, tenant, scope) consent.
- Deterministic single-destination selection.
- Server-minted replication assignment binding customer/source/destination/provider/backend/keys.
- Destination-minted upload, destination-side verification, and a provider ATTESTATION the origin trusts.
- Idempotent status recovery for lost responses; read/delete against the owning provider.
- Customer billing + provider settlement from the persisted assignment.
- Two-Foghorn integration coverage.

## Non-Goals (later — `placement-policy-engine.md`)

- Multiple durable copies / `artifact_locations`; multiple backends per cluster; RAID/HDD storage nodes.
- Replica counts / durability policies; region / residency / cost / tier selection; backend migration.
- `.blocks`, edge caches, hot/warm/cold optimization.

## Proposal

1. **Entitlement.** `tenant_cluster_access` gains explicit operation scopes (e.g. `operations TEXT[]` ⊇
   `{serve, store, replicate}`) — do NOT overload `access_level` (a capacity tier). Enforce `is_active` +
   `subscription_status='active'` + `expires_at`. `GetClusterRouting.cluster_peers` carries the operations.
2. **Provider consent.** `infrastructure_clusters` advertises a writable durable-storage capability
   (backend kind + endpoint + free capacity/health). Consent is per (provider cluster, tenant, scope)
   approval — a global `accepts_tenant_storage` flag alone is insufficient.
3. **Destination selection.** explicitly-subscribed durable cluster (store/replicate + consent + writable +
   healthy) → tenant official cluster (today's path) → fail closed.
4. **Assignment.** Server-minted record: assignment id, customer tenant, artifact id/kind, source
   node/cluster, destination cluster, storage-provider tenant, backend id/kind, exact object keys (main +
   `.dtsh`), expected size/checksum, expiry, status. The node chooses none of these.
5. **Mint + verify + attest.** The DESTINATION Foghorn mints the presigned PUT for its own backend and, on
   completion, HEAD-verifies the exact keys and returns an attestation (observed bytes + ETag/version/
   checksum). Origin commits `synced` only on a valid attestation, never on the node's word.
6. **Recovery.** Lost responses are recovered through an idempotent status/query RPC against the assignment
   id — never by repeating an unauditable mint/upload.
7. **Read / delete.** Prepare/playback and deletion for a remotely-stored artifact address the OWNING
   provider (descriptor, not a local S3 URL).
8. **Attribution.** Cold-usage aggregation + the storage snapshot read the persisted assignment
   (destination cluster, provider tenant, backend, provider-observed bytes) instead of inferring provider
   from the emitter. Customer billing rates the usage tenant; provider settlement credits the storage
   provider (see [`federated-settlement-attribution.md`](federated-settlement-attribution.md)).

## Impact / Dependencies

- **Cluster-bound service identity is a hard dependency.** Federation currently authenticates with a shared
  static `SERVICE_TOKEN` over server-authenticated TLS and only checks `auth_type == service` — it has NO
  transport mTLS or cluster-bound client identity, so it cannot root a provider attestation. This RFC
  DEPENDS ON [`service-identity-and-cluster-binding.md`](service-identity-and-cluster-binding.md) (service
  JWTs with non-forgeable `cluster_id` claims). Until that lands, the attestation MUST be a payload signed
  by a cluster-bound issuer key with a defined issuer/rotation model — not "the authenticated connection".
- Schema: `tenant_cluster_access.operations`, `infrastructure_clusters` writable-capability columns, a
  `storage_replication_assignments` table (Foghorn). Proto: Quartermaster entitlement/consent RPCs, a
  federation replication-mint/verify/status RPC set, the attestation message.
- Services: Quartermaster (entitlement + consent), Foghorn control + federation (assignment, cross-cluster
  mint/verify/attest/status), billing (`GetColdStorageUsage` + snapshot read from the assignment).
- `placement-policy-engine.md` depends on this protocol (it selects among destinations this contract mints).

## Alternatives Considered

- **Origin mints for the remote bucket via delegated credentials.** Rejected: leaks provider credentials
  across trust domains and gives origin write authority it should not hold.
- **Trust the existing `SERVICE_TOKEN` connection as the attestation authority.** Rejected: the shared token
  is not cluster-bound, so any holder could forge a "verified" attestation for any cluster (F7).
- **S3 Object Lock / versioning for immutability instead of an attestation.** Insufficient for cross-cluster:
  origin cannot verify a remote object it has no credentials for.

## Risks & Mitigations

- **Forged attestation** → cluster-bound identity dependency above; fail closed without a valid attestation.
- **Lost completion / double-charge** → idempotent status RPC keyed on the assignment id; billing reads the
  assignment, not per-message events.
- **Provider capacity exhaustion mid-upload** → consent advertises free capacity/health; destination rejects
  the mint when unwritable; origin keeps the artifact local (routable) on rejection.

## Migration / Rollout

1. Ship the truthful single-provider (local/official) release, then canary.
2. Land cluster-bound service identity (dependency).
3. Build this RFC behind the entitlement/consent gates (no tenant is opted in until a `store`/`replicate`
   grant + provider consent exist), so enabling it is a data change, not a code toggle.
4. Then build the Placement Policy Engine on top.

## Open Questions

- Attestation format/rotation before cluster-bound JWTs land (signed payload issuer model)?
- Does settlement reconcile against provider-reported usage, or solely the origin-persisted assignment?
- Retention/GC of `storage_replication_assignments` after terminal states.

## References, Sources & Evidence

- `api_balancing/internal/control/server.go` (`processFreezePermissionRequest`, `processSyncComplete`),
  `api_balancing/internal/control/freeze_authz.go`, `api_balancing/internal/control/storage_usage.go`.
- `docs/rfcs/service-identity-and-cluster-binding.md`, `docs/rfcs/placement-policy-engine.md`,
  `docs/rfcs/federated-settlement-attribution.md`.
- `docs/architecture/tls.md` (server-authenticated TLS + `SERVICE_TOKEN` S2S model).

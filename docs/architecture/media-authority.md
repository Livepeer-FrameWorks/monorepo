# Media-cluster authority and autonomy

FrameWorks media cells make media-serving decisions from signed, durable local
authority. Commodore compiles the control-plane state owned by Quartermaster,
Purser, and Commodore into versioned Ed25519 envelopes; every target Foghorn
verifies and persists those envelopes in its cell database before acknowledging
delivery. Helmsman and Mist consume decisions and configuration from Foghorn;
they do not become tenant-policy authorities themselves.

The availability rule is deliberate: a control-plane outage does not invalidate
a still-valid local decision. The local path never interprets missing or corrupt
state as an allow; it may ask the connected authority when available. A signed
revocation or tombstone remains a denial, and hard expiry remains unavailable;
none can be overridden by connected fallback.

## Authority contents

Tenant authority contains lifecycle, billing decision/model, preferred and official clusters,
serve/work grants with provenance and expiry, limits, allowances, and DVR policy.
Preferred and official roles are surfaced only while the named cluster remains active and
effective for serving; a raw tenant reference to a deactivated or revoked cluster is metadata,
not serving authority.
Media-object authority contains tenant/user identity, the externally shareable playback locator and
internal names, lifecycle, origin cluster, playback policy, and either live-stream
or artifact identity. Live authority additionally contains the publishing-key
digest, deterministic outage-ingest owner, process/DVR configuration, and sealed
source/output configuration. Artifact authority identifies the stable hash, kind,
parent stream, and the parent's Mist routing name. The playback locator is not an
authorization decision: public, JWT, webhook, and deny policy remain explicit in
the same signed object.

The publishing credential is never stored in plaintext at Foghorn. Webhook
credentials, pull URIs, native input definitions, and push target URIs are
encrypted independently for active recipient cells with X25519 + HKDF +
AES-GCM. Historical delivery cells receive signed replacements or revocations
but do not receive newly issued secret boxes.

Signing and encryption keys are separate:

- Commodore receives the Ed25519 signing private key and the public X25519
  recipient map.
- Each Foghorn cell receives the Ed25519 trust set and only its own X25519
  private key.
- Helmsman receives neither.
- The seal root is CLI provisioning input and is not rendered into a workload.

## Compilation and delivery

Purser and Quartermaster mutations write media-authority refresh outboxes in the
same database transactions as their owner state. Commodore stream, artifact,
policy, key, source, process, DVR, and output mutations likewise enqueue or
compile replacement authority. Commodore allocates a monotonically increasing
version and commits the signed envelope, history, durable every-targeted-cell
ledger, and one delivery obligation per target cell atomically. Owner outbox
generation fences ensure a mutation committed during delivery survives the
older worker's completion attempt.

Compilation is fenced per signed authority across Commodore replicas: tenant
compiles use `tenant:<tenant_id>`, while every live stream or artifact uses its
exact `media_object:<authority_id>` key. A worker allocates
a generation before owner reads, then verifies that generation while holding
the fence row only inside its short persistence transaction. A newer compile
of the same authority therefore prevents an overtaken compile from committing,
without making unrelated objects from one tenant invalidate each other or
pinning a database connection across Quartermaster/Purser RPCs. Retry backoff
includes deterministic per-row jitter so a superseded batch does not reconverge
as another synchronized collision.

Push delivery is the fast path. Owner refresh and per-cell delivery have
independent worker loops, so a blocked cell cannot stop authority refresh.
Each remote delivery operation—including Quartermaster discovery, Foghorn
resolution, and apply—is bounded to 35 seconds. Durable acknowledgement then
gets an independent five-second settlement budget, so a slow cell cannot spend
the database acknowledgement's deadline. Refresh compilation is bounded to 90
seconds per claimed row and failure settlement likewise escapes the expired
work context. A claimed batch contains at most one eight-worker wave and
therefore stays below its two-minute lease.
Per-cell obligations lease and retry
independently, and a newer version terminally supersedes older pending work. On
Foghorn registration/reconnect, Foghorn requests replay by its explicit control
cell ID, never by a virtual cluster ID. Periodic reconciliation/backfill covers
a missed source event. A cell acknowledges only after signature, schema,
digest, audience, time, invariant, and monotonic-version checks pass and the
envelope plus decoded indexes commit to the Foghorn database.

Foghorn authority apply uses the fixed `maut` two-key advisory namespace and a
five-second transaction-local `lock_timeout`. Artifact projection first takes
the shared thumbnail asset lock, then the authority lock. A hot purge therefore
fails one apply attempt into the durable delivery retry instead of consuming a
pool connection for the entire caller deadline; unrelated authority kinds
cannot create a new advisory namespace from signed data.

Every Foghorn replica in a control cell reads the same durable projection.
Process restart therefore reloads authority from PostgreSQL rather than waiting
for Commodore. A lower version is rejected; the same version with a different
digest is a conflict; the same version and digest is an idempotent success.

The local control-session identity is durable too. After Quartermaster has
authenticated a Helmsman fingerprint, Foghorn stores the canonical node,
tenant, cluster, and Ed25519 node-key binding in the cell database with a
bounded validity interval. Every registration signs the asserted node ID,
stable fingerprints, public key, timestamp, and a fresh nonce; Foghorn verifies
the signature and durably rejects nonce replay before publishing connection
state. A later Foghorn restart may reuse the still-valid binding when
Quartermaster is unavailable, including when its client circuit breaker is
open. It is keyed by the exact machine-id or MAC signal Quartermaster matched,
never by source IP or the node ID asserted by the client. A live Quartermaster
`NotFound` or `PermissionDenied` deletes the matching local admission and cannot
fall back to it; first enrollment still requires the control plane and cannot
replace another node's tenant/key/fingerprint binding. Foghorn bounds concurrent
pre-authentication Quartermaster work; saturation is an unavailable-authority
condition for already admitted nodes, while new enrollment retries safely.

The v0.3 upgrade is additive for existing fingerprint rows. A legacy row with
no node key is not local outage authority. On its first post-upgrade online
machine-ID or MAC resolution, Foghorn sends the public key whose registration
proof it already verified and Quartermaster atomically fills the binding only
when it is still null. A different existing key is an authoritative rejection,
and peer-IP fallback never seeds the key. Foghorn persists local admission only
after the response returns the same key, so the node needs that one successful
control-plane resolution before it can reconnect during an outage.

Helmsman's identity record is node-bound and lives under
`HELMSMAN_STATE_DIR/node-identity`, never on the reclaimable media volume. A
legacy media-volume seed is migrated once. Copying a persisted record to a
different `NODE_ID` is a startup error. If the durable state is genuinely lost,
the supported recovery is `frameworks edge provision --ssh <user>@<host>
--force-reenroll --enrollment-token <fresh-token>` (use `--local` for the local
host): the signed registration explicitly requests rotation, and
Quartermaster replaces the pinned public key only when tenant, node, cluster,
and at least one supplied stable machine/MAC fingerprint still match. Without
that explicit token-authorized request, a different key is rejected. The local
record binds the one-shot request to the fresh token, reuses the same
replacement key while registration is pending, and marks it complete only
after Foghorn accepts the registration; a restart cannot accidentally rotate
again from a still-rendered flag.

## Request-path behavior

| Path                                                       | Local decision and state                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                             |
| ---------------------------------------------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------ |
| `PLAY_REWRITE`, HTTP `/play`, gRPC `ResolveViewerEndpoint` | Playback/internal identity, tenant decision, serving grants, and public/JWT/webhook policy come from the same verified object+tenant version. Artifact placement uses Foghorn inventory/database state without re-resolving catalog metadata.                                                                                                                                                                                                                                                                                                                                                                                        |
| `USER_NEW`                                                 | Rechecks the signed policy and authenticated node's local cluster binding before registering viewer/capacity side effects. A successful local JWT verification performs a bounded, non-blocking enqueue keyed by `(tenant, kid)`; one coalescing writer advances Foghorn's revision-fenced durable outbox off the viewer path. Commodore's `last_used_at` projection catches up after reconnect without becoming a playback dependency.                                                                                                                                                                                              |
| `PUSH_REWRITE` and ingest endpoint resolution              | Verifies the publishing-key digest and tenant authority locally. Endpoint resolution first asks Commodore for the current live-claim placement; if that connected lookup is unavailable, a ready signed projection supplies the fallback and pins only to its deterministic outage owner. `PUSH_REWRITE` likewise retains the connected distributed claim in normal operation; on central failure, only the signed outage-owner cluster may admit a new publisher. Existing durable sessions continue.                                                                                                                               |
| `STREAM_SOURCE` and `/source`                              | Uses signed pull/native/artifact identity and cell-opened secrets, then local/federated runtime placement. The caller's authenticated node cluster—not Foghorn's process cluster—identifies the destination.                                                                                                                                                                                                                                                                                                                                                                                                                         |
| `STREAM_PROCESS`                                           | Reads process JSON stamped onto the durable ingest session, processing job, or rolling DVR. It remains a one-shot Mist trigger; absence is a job/admission error, not a polling condition.                                                                                                                                                                                                                                                                                                                                                                                                                                           |
| Multistream/output status                                  | Admission retains the exact target payload and authority version as row-bound v2 ciphertext under `FOGHORN_STATE_ENCRYPTION_KEY`. Required status and placement updates are durable local outboxes and drain after reconnect. Push status upserts carry Mist event time, reject older observations, and prefer terminal status on a known-time tie. Legacy observations with no event-time header use arrival order instead of treating zero as a real shared timestamp. The credential-bearing payload is cleared when its source generation ends or is poisoned. During a rolling Foghorn upgrade, `_v2` states fence old workers. |
| Config restart                                             | Foghorn locks the per-node seed row, loads the latest durable payload, allocates the next `ConfigSeed` version, applies the producer's mutation/fallback merge, and persists that exact payload in one transaction. Helmsman persists/applies last-good state. An authenticated reconnect reporting a positive applied version keeps that local seed if Foghorn cannot persist a replacement. A fresh or unconfigured node is rejected and remains unroutable. Missing central components preserve only unresolved fields, while authoritative empty fields remove stale state.                                                      |

Helmsman persists processing overrides as job-owned, versioned records. Startup
quarantines an unreadable record without abandoning valid neighbors. An elapsed
record deadline is not itself proof that the job ended: Helmsman preserves the
policy when Mist still owns the processing stream, removes it after Mist
authoritatively reports the stream absent, and preserves it when Mist cannot be
queried. The same check repeats every minute, so an expired orphan does not live
forever merely because Mist was unavailable during startup. Normal terminal
handling clears the record unconditionally.

Marked local authority is a one-time consumer/schema compatibility cutover, not
approval of one payload version. Once a projection is ready, successfully
verified replacement versions preserve the marker; signature, schema, digest,
scope, time, and payload invariants are checked on every apply. Signed denial,
tombstone, hard expiry, and a ready authority missing a required sealed policy
secret are terminal local outcomes (hard expiry reports unavailable; the others
deny). A transient local database/driver failure,
corrupt row, or inconsistent projection is not an authoritative denial: while
the connected evaluator is available, trigger, HTTP, and gRPC paths use it;
otherwise they return unavailable. An unmarked projection may shadow-compare against
connected behavior during rollout.

Shadow comparison uses a canonical authority projection, not whole transport
messages. Connected responses expose health-filtered `cluster_peers` for
routing and separate `authority_cluster_peers` for the stable tenant grants.
Comparators normalize ordering and nil/unlimited limits, select the same
path-specific enforcement-cluster override on both sides, and compare only policy fields
both paths derive. Missing authority fields during a rolling upgrade leave the
projection unmarked; they never turn a mismatch into permission or denial.
The local path preserves the same split: it derives authority peers only from
signed grants, then overlays Foghorn's live PeerChannel connectivity and
addresses to build routing peers. A disconnected grant remains valid authority
but is not a routing candidate. Preferred/official roles come from signed tenant
identity; health and addresses never do.

The compatibility tenant-cache invalidation RPC may still accelerate old mixed
cells. It clears only legacy connected caches; it cannot delete, replace, or
extend signed authority. Signed replacement delivery is authoritative.

## Validity and outages

Tenant and live-stream envelopes refresh after ten minutes and hard-expire no
later than 24 hours. Artifact envelopes hard-expire no later than seven days,
and are also capped by their tenant authority. Before `refresh_after`, Foghorn
decides locally. Between refresh and hard expiry it continues deciding locally
and requests one background refresh. A failed refresh does not shorten the
signed validity interval.

At hard expiry the local decision is unavailable. Signed denials and tombstones
remain denials; delivery to every historically reached cell prevents a removed
cell from retaining newly valid secrets. A hard-deleted tenant is compiled from
its last version into a tenant tombstone and delivered to that same historical
target set; it is not retried forever as an unresolved owner lookup. The
unavoidable revocation bound is the time between an owner mutation and
replacement delivery, capped by the old envelope's hard expiry. Pending
delivery, version lag, apply rejection, and local freshness are operational
signals, not permission defaults.

New x402 settlement and other new control-plane mutations remain explicitly
online-only. Bootstrap, node adoption, catalog management, and creation of a new
processing job are management paths, not outage media-serving paths.

### Known limitations

- **Asymmetric partitions can admit two publishers.** The signed outage owner
  is deterministic, but it cannot observe a still-live claim isolated in
  another cell. If that cell can keep serving while the outage-owner cell loses
  the control plane, both may temporarily accept ingest until the partition or
  the remote claim/lease converges. The authority model bounds and identifies
  the fallback owner; it is not a cross-partition consensus protocol.
- **A lost liveness signal eventually releases authority.** The cross-cell
  active-ingest placement claim is a 30-second lease that Foghorn renews every
  5 seconds for every unended PostgreSQL ingest session, including a pending
  generation whose blocking admission is still being confirmed. Within a cell,
  publisher identity and tenant stream capacity come from PostgreSQL ingest-session rows; Valkey expiry cannot revoke a
  publisher. `PUSH_INPUT_CLOSE` is the exact PID/generation finalizer.
  Event-time-fenced `STREAM_END`, node-disconnect reaping, projection abort,
  and placement-claim loss are backstops that end only the matching durable
  generation. Helmsman's Mist inventory supplies the local runtime backstop:
  it compares the admitted connector PID with Mist's `sourcepids`, so a viewer
  or replacement input retaining the same runtime name cannot mask a dead
  publisher. Mist exposes `sourcepids`; the FrameWorks fork additionally
  preserves an observed empty PID set as `sourcepids: []` instead of serializing
  it as `null`. Helmsman treats an array as authoritative, while a missing,
  `null`, or malformed member remains fail-open and is counted by payload
  shape. This is a permanent compatibility property of unforked or older Mist,
  not a temporary rollout mode: those nodes do not provide authoritative PID
  absence and therefore cannot use this reaper as a disconnect backstop. Polls
  run every ten seconds. No
  absence dwell is accumulated during the first 30 seconds after admission;
  after that grace, three consecutive authoritative misses emit the exact
  generation and PID for a fenced reap, capped at 64 reports per poll,
  so the normal lower bound is about 60 seconds (30 seconds of admission grace,
  then three 10-second missing observations), including when Mist dies while
  the control stream remains connected. An exact replay of the same
  generation and connector PID preserves the original admission timestamp and
  cannot re-arm that grace window. If the
  entire liveness path is isolated, the cross-cell placement lease can expire
  and another cell may admit a publisher even while the unreachable cell keeps
  its old durable session.
- **The concurrent-stream cap is enforced per media cell.** The durable
  PostgreSQL count covers all Foghorn replicas sharing that cell database; it
  is not a tenant-global cross-cell counter. Cross-cell placement still limits
  one active owner per stream, but a tenant publishing distinct streams into
  multiple cells can consume the configured cap in each cell.
- **Unattributed events cannot mutate tenant state.** A lifecycle event whose
  tenant cannot be resolved is recorded/diagnosed but cannot safely release or
  rewrite tenant-scoped authority. Exact session identity, durable mappings,
  tombstones, and bounded reapers provide convergence where they have enough
  identity; the system does not guess a tenant from a stream-shaped string.
  In particular, a tenantless `STREAM_END` remains in Helmsman's durable WAL and
  is retried without a fixed attempt cap. That is intentional: acknowledging an
  unattributed close could strand the session forever, while applying it to a
  guessed tenant could release another tenant's authority. Operators must treat
  sustained WAL growth as an identity-resolution incident.
- **Federated pointers are a replaceable routing cache, not local storage
  authority.** A signed object tombstone lets the receiving cell remove an old
  routing pointer while the durable tombstone remains as the resurrection
  fence. A pointer can own regenerable cell-local derivatives such as thumbnails
  and disposable cache copies even though it never owns the remote parent bytes.
  Purge first verifies that this cell can execute every recorded destination,
  then installs a durable token and three-minute lease under the shared
  thumbnail/authority asset lock before sweeping derivative bytes using their
  recorded backend. A destination-less legacy pointer is considered local only
  when its recorded backend fingerprint exactly equals this cell's immutable
  backend fingerprint; missing or foreign evidence keeps the pointer terminal
  and defers repair rather than guessing a store. The pointer remains
  terminal while remote cleanup is in flight; authority apply cannot make it
  routable around an unlocked byte sweep. On success, token ownership is
  rechecked under the asset lock and thumbnail control rows are removed in the
  same transaction that either deletes the pointer or restores newer active
  authority with truthful `has_thumbnails=false` state. A failure keeps the
  pointer terminal and makes the lease immediately reclaimable because byte
  effects may be partial or unknown. Both scheduled discovery and short-cadence
  recovery run up to eight candidates concurrently
  with independent two-minute cleanup budgets and independent five-second claim
  settlement budgets, so a slow destination cannot consume the pass or strand
  later claims. Every expired tombstone, stale, or newer-active claim is
  rediscovered on the 30-second local cadence rather than waiting for daily
  retention discovery. Authority application takes the same asset lock before
  projecting artifact lifecycle, closing the absent-row snapshot race in both
  commit orders. A dedicated `federated_purge_eligible_at` clock, initialized
  when the pointer is created or restored and reset by an unfenced tombstone,
  is the only age input to discovery. Metadata, inventory, access, thumbnail,
  and re-adoption writers cannot postpone it. A tombstone delivered while
  cleanup owns a token updates signed authority but cannot reset this clock.
  Live node copies or either chapter-ledger
  artifact reference retain the pointer instead of being cascaded or nulled
  away. A signed object tombstone is terminal: a higher active object version is
  rejected rather than resurrecting the authority.
  If no tombstone arrives, the normal purge job may evict a pointer past its cache-age threshold
  only after the cell has no unexpired active signed authority for that tenant
  and artifact. It never infers deletion from peer reachability, never deletes
  the remote parent bytes, and a later valid federation response may recreate
  the pointer. Creating derivative bytes such as a thumbnail does not promote
  the remote parent artifact. A path that creates locally owned artifact bytes
  must explicitly promote that artifact out of pointer state; chapter
  finalization currently performs that promotion.

## Configuration and restart boundary

Development compose uses generated environment defaults and Helmsman's runtime
reconciler; operators must not hand-edit Mist configuration. Production uses the
CLI service renderer (`buildServiceEnvVars`), not `docker-compose.yml`: it scopes
signing material to Commodore and distinct per-cell verification/decryption
material plus an explicit `MEDIA_AUTHORITY_CELL_ID` to each Foghorn. Production
rendering fails when these inputs are absent. The recipient key ID binds the
control-cell ID and X25519 public key; Commodore and Foghorn recompute that
binding at startup, so a cell/key mix-up fails before serving. A renderer test
rejects root-secret leakage and cross-cell private-key reuse.

On steady-state restart, Foghorn reads its committed storage identity, signed
authority, config seed, process/session state, output obligations, and admitted
node bindings locally. Quartermaster service registration gets one short startup
attempt and then reconciles in the background. Served-cluster refresh and
Commodore authority replay are repair work, not listener prerequisites. A true
first boot still fails closed when Quartermaster is needed to establish the
immutable storage descriptor or enroll a node.

Helmsman owns the complete managed Mist trigger set and repairs drift after
restart. Mist trigger transport and failure actions are specified separately in
[Mist trigger contract](mist-trigger-contract.md); `defaultStream` is not a
policy fallback.

Helmsman's durable state root is separate from hot media. It contains the
ConfigSeed, trigger WAL, ingest fences, node identity, and processing overrides.
Container production mounts `/data/state` independently from `/data/storage`;
native production renders the role's `helmsman_state_dir`. Losing or cleaning
the media volume must not erase control identity or last-good configuration.

## Operations and incident response

The v0.3.0 rollout repairs both sides of the artifact seam without bulk row
rewrites in expand DDL. The catalogued
`commodore_dvr_playback_authority_v0_3_0` migration snapshots parent-stream
policy onto existing DVR rows. The dependent
`commodore_chapter_playback_authority_v0_3_0` migration then snapshots the
parent stream policy onto existing chapter VODs by their durable `stream_id`;
legacy rows do not depend on a populated `dvr_hash`. During a rolling deploy, a
parent DVR whose authority snapshot is not ready falls back to the parent stream
exactly as runtime readers do; an old replica cannot make the migration snapshot
an unready fail-closed placeholder. Dependency ordering is declared and both are
required before `postdeploy`. A worker verifies its invariant before marking a
short `SKIP LOCKED` batch complete, so rows hidden by a concurrent lock cannot
produce a false green migration state.
Foghorn cells own separate physical databases, so each Foghorn runs the bounded,
`SKIP LOCKED` `foghorn_federated_artifact_lifecycle_v0_3_0` and
`foghorn_federated_pointer_purge_eligibility_v0_3_0` repairs against its own
database in the background. The second preserves a pre-existing pointer's age
when the dedicated eligibility clock is introduced instead of granting every
RC-era pointer a fresh retention window. HA replicas cooperate and verify each
cell-local backlog before stopping. Both use the same durable migration job/lease/checkpoint
machinery as operator-run migrations. It is intentionally not represented as a
single control-host release gate: such a gate cannot prove completion in every
independent cell database. A signed object-authority tombstone fences both new
federated adoption and migration repair. Pointer eviction is a separate cache
lifecycle: tombstoned pointers are removed after their retention age, and a
pointer past its cache-age threshold with no unexpired active signed authority
may be evicted even when no tombstone arrived. Before deleting the routing row,
Foghorn terminally fences it under the thumbnail asset lock, sweeps its local
derivatives, removes thumbnail control rows, and refuses the purge while live
artifact-node or DVR-chapter dependencies remain. It does not remove the durable
authority tombstone/version fence or remote artifact bytes. A durable purge
token/lease keeps the pointer terminal across byte cleanup and process crashes;
the 30-second recovery loop resumes expired tombstone, stale, and active-restore
claims with bounded concurrency and per-candidate deadlines. The recovery loop
has its own goroutine; the scheduled pointer pass uses an independent
lifecycle-cancelled budget and the same bounded pool after locally owned byte
cleanup. Only token-fenced
successful settlement may delete it or restore newer active authority with
cleared thumbnail state. Re-adoption and ordinary artifact metadata updates do
not refresh `federated_purge_eligible_at` or extend signed validity.

Monitor authority delivery backlog/version lag, verification/apply failures,
fresh/soft-expired/hard-expired local reads, shadow mismatches, durable output
outboxes, signing-key-use delivery warnings,
`foghorn_admission_payload_crypto_total{format,result}` (legacy reads must trend
to zero), `foghorn_node_admission_events_total{operation,result}`, and
`foghorn_media_request_central_rpcs_total{path,service,method}`.
The last metric is an autonomy regression guard. Playback and source paths
covered by ready local authority produce no central attempts during an outage.
Ingest endpoint discovery deliberately makes one bounded Commodore placement
attempt so a live claim can override the signed outage owner; after that attempt
fails it resolves entirely from still-valid local authority and runtime state,
without Quartermaster or Purser calls.

Foghorn retains apply/verification audit observations for 30 days and deletes
expired rows in bounded local batches. This diagnostic retention never changes
the signed current authority or its decision.

For signing-key rotation, publish the next public key to every Foghorn trust set
before activating its Commodore private key. Keep the previous public key until
all still-valid envelopes signed by it have expired or been replaced. Local JWT
verification continues advancing `last_used_at` through Foghorn's durable
outbox during a Commodore outage. For cell
seal rotation, v0.3.0 has no overlap keyring: each cell accepts one active
recipient. Stop compilation, replace the recipient/private-key pair in a
coordinated maintenance window, recompile every secret-bearing envelope for the
cell, and verify convergence before restoring outage-serving guarantees. Never
reuse either key family for the other purpose.

During a suspected signer compromise, remove the key from trust, stop authority
compilation with it, activate a pre-distributed replacement, and force complete
tenant/object recompilation. During a cell seal-key compromise, rotate that cell's
recipient, recompile all secret-bearing authority targeted there, and revoke or
drain the affected cell as required. Do not extend validity or enable central
fallback to mask a key incident.

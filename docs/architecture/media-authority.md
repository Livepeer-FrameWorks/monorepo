# Media-cluster authority and autonomy

FrameWorks media cells make media-serving decisions from signed, durable local
authority. Commodore compiles the control-plane state owned by Quartermaster,
Purser, and Commodore into versioned Ed25519 envelopes; every target Foghorn
verifies and persists those envelopes in its cell database before acknowledging
delivery. Helmsman and Mist consume decisions and configuration from Foghorn;
they do not become tenant-policy authorities themselves.

The availability rule is deliberate: a control-plane outage does not invalidate
a still-valid local decision. The local path never interprets missing or corrupt
state as an allow. A cell holds the authorities that are in use; one it does not
hold, or holds past its validity, it asks Commodore for, applies, and then
decides on locally. A signed revocation or tombstone remains a denial and is
never asked around. When Commodore cannot be reached, an authority past its
validity is refused.

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
pinning a database connection across Quartermaster/Purser RPCs. Losing the
fence is not a service outage: the overtaken obligation stays pending and retries
after 30 seconds without consuming its failure budget. Fetch contention retries
within one bounded deadline, then returns `Aborted`, without opening the cell's
shared outage backoff for unrelated objects.

### Refresh obligations

Refresh work is bounded by the number of authorities, not by event volume or by
how long something has been failing. `commodore.media_authority_refresh_obligations`
holds one row per target and lane. A target is a tenant authority
(`tenant:<id>`), a media object (`media_object:live_stream:<id>`,
`media_object:artifact:<id>`), or the set of a tenant's objects
(`tenant_media_objects:<id>`); its key is also the compile-fence scope. Lanes are
`event` for source changes, `bulk` for work enumerated from a set (a tenant's
objects, the tenants to reconcile), and `tenant_deadline` / `object_deadline` for
scheduled renewal, each with its own worker, lease, and timeout. A tenant change
that touches thousands of objects therefore never stands in front of a change to
one of them. Each lane is drained by one claimant feeding a fixed number of
compile slots, claiming again as soon as a slot frees, so a lane's rate is what
its compiles take and one slow compile holds one slot. Object compiles share the
Purser answers a tenant's objects all derive from (its subscription and tier)
across claims, so a fanout asks Purser about a tenant a constant number of times
rather than once per object. Every claimed row carries `triggered_at`, its last
write before the claim, and `claimed_at`, read after the claim's snapshot, both
by the database clock. An answer is stamped with the `claimed_at` of the claim
whose compile read it, and serves a row only when the row's `triggered_at` is at
least 500 ms (the clock skew YugabyteDB tolerates between nodes) before that
stamp, so a compile never uses Purser state from before the change that enqueued
it. Purser commits a change before the event that enqueues its refresh, which is
what makes the rule hold; answers age out after two minutes.

Every writer goes through `commodore.enqueue_media_authority_obligation`: source
triggers, owner-service events, placement and playback-policy changes, fanout,
and renewal scheduling. A second event for a target folds into its existing row
and bumps `revision`. Completion is fenced on the claimed revision, so an event
that folds in while the row is compiling leaves it pending instead of being
completed away. A target is never compiled by two lanes at once. Every claim
writes the target's row in `media_authority_target_claims`, and a claim finding
that row held by another lane under a live lease skips the target; settlement
deletes it. A check of the other lane's lease alone would not do: it reads a
snapshot, and two lanes can both pass it before either commits. Writing one
row serializes them — on PostgreSQL the second waits and then sees the first's
claim; under Yugabyte's snapshot isolation the second fails and the next pass
sees it. Folds keep a live obligation lease because a fold flips the row to
pending while its compile is still running. Event and bulk leases are ten
seconds, renewed every third of that on the obligation and the claim row while
the compile runs, so a replica that stops mid-compile (a restart, or a claim
that committed after its caller gave up) holds the target for at most one
lease, and a change that folded into its row runs within seconds. Without this, an event compile and a
renewal of the same authority both run, and the compile fence discards one after
its source reads and signing. Each claim carries a fresh token on the
obligation and the claim row, and every settlement and release requires it: a
worker whose lease lapsed, and whose target was claimed again at the same
revision, settles and releases nothing. A compile that loses the fence to
another compile of the same authority (a fetch, say) is not complete: that
compile has only started and may still fail, so the row goes back to pending
and runs once more, a no-op if the other compile published. A waiting event keeps its due time when its target changes again, so a
target that keeps changing is not sent to the back of a backlog; a renewal's due
time is replaced by each publication.

A compile failure is classified. A failure that retrying cannot fix — a source
row that cannot produce a valid envelope, an undecodable policy, a missing seal
recipient, a cell without the required capability — parks the target after one
attempt with a `park_reason`. A transient failure backs off with deterministic
jitter and parks as `exhausted` after twelve attempts. A media object whose
tenant has no published authority requests that authority and parks until it
exists. A parked target costs nothing until a new event for it arrives or the
hourly reconciler re-arms it; it is never retried on a timer by itself. The
reconciler re-arms an `exhausted` target with three attempts left, so a long
outage costs three attempts per pass rather than a full retry budget; a target
parked for any other reason waits on its source changing and starts over.

Compiler errors are not access decisions. Missing recipients, unavailable
decryption keys, unchanged source that cannot compile, or dependency outages leave the previous valid
signed copy intact and alert through refresh obligations. A removed publishing
credential, tighter playback policy, or changed placement restriction can revoke
the old allow independently of billing or processing. Adding JWT signing keys,
widening accepted audiences, or relaxing claims does not revoke existing access.
An appended placement fallback or a wider selector/distance limit also retains
existing access. Playback publications record a digest of the stored access
configuration (including the encrypted webhook credential). A failed compile of
a changed malformed policy or webhook configuration cannot silently retain the
previous allow. An unchanged configuration that this compiler cannot interpret
does not imply a new revocation. Failure to read the comparison state emits a
dedicated `MediaAuthorityRevocationCheckFailed` alert and an authority-scoped log.
Revocations have a separate bounded write deadline, retaining compile and parent
fences, so a dependency consuming the compile budget cannot suppress them.
The comparison is installed as soon as the object's source is loaded, before
tenant lookup, so an unavailable parent cannot hide an explicit access change.
Failed revocation persistence stays retryable. An unreadable fallback snapshot
does not erase the original retry or parent-wakeup classification. A quote whose
entitlement digest differs from the captured tenant waits for refreshed tenant
authority; this cross-service race is not malformed source configuration.

Current target cells and correction-only recipients are separate. When a cell
leaves the target set, its correction horizon is frozen at the latest validity of
any version previously sent there, including versions with lost acknowledgements.
Only that cell's signed envelope is capped at the horizon; active recipients
retain their normal leases and renewal schedule. The immutable delivery cap is
used for claims, fetch, replay, acknowledged inventory, and recovery, including
lost ACKs. It survives a later regrant without changing historical envelope
validity. Publications lock recipient target rows until enqueue; pruning skips
those locks. Renewals cannot keep a removed cell alive indefinitely. Expired retired target rows are
pruned in indexed, bounded batches; version counters and recovery fences remain.
A later explicit grant reactivates the cell and publishes an uncapped delivery,
even if the combined active/correction recipient set is unchanged. Object compiles inherit only the
tenant's active targets, never its historical correction recipients.

A live stream that is not ingesting takes its origin from the tenant's preferred
cluster, the same route origin connected validation reports. A stream with
neither has no origin and compiles without a publishing credential: the cell
finds no local credential for its stream key and validates it against Commodore,
exactly as for a stream it has never seen. Only outage-time ingest, which has no
cluster to bind to, is unavailable for it.

### Publication

A compile publishes a new version only when it would say something new. Inside
the publishing transaction, after the fence is locked, the compiled authority is
compared with the current version and published for exactly one of four causes:
its `content` differs, its target `cells` differ, its `validity` would end
earlier, or its `renewal` is due and would extend validity. Anything else is a
no-op that writes no version, no delivery, and no fanout, so reconciliation and
redundant events cost one read. A tombstone is terminal: it follows its target
cells and is renewed while a valid older copy still needs correction. It stops
renewing once no such copy remains. Cell sets are compared in
byte order on both sides, never in the database's collation order.

A live authority never rests on a settled renewal, because a cell refuses a
hard-expired authority outright. Every no-op compile outside the renewal lanes
restores a missing or settled renewal and leaves a live one untouched. A renewal
that loses the compile fence to an event compile stays pending and runs again:
the event compile may have published nothing. The stream process configuration
an object carries is read from Purser and is part of its content, so a compile
that cannot reach Purser fails and retries instead of publishing an empty
configuration.

Content identity is `content_digest`. Tenant payloads carry no per-compile
entropy, so their deterministic encoding is their content. A media object seals
secrets with a fresh ephemeral key and nonce on every compile; its digest covers
the payload with the sealed boxes removed plus a commitment to each sealed
secret's plaintext and recipient set, keyed from the signing key. Two compiles of
identical state digest equal, a rotated secret or recipient does not, and rotating
the signing key re-issues everything. Objects carrying commercial quotes have no
stable identity, because quote observation and expiry are part of what they say,
and always publish.

A tenant publication fans out to the tenant's media objects only when
`dependents_digest` — the tenant fields objects derive from, which excludes
allowances, limits, and decision text — changes (and, while some cell still takes
only short-lived object authorities, when tenant validity shrinks below what
objects were capped to). The fanout obligation is written in the tenant's
publishing transaction. Processing it reads, once, the tenant's objects cells may
still hold — the current version is not a tombstone and some version of the
object is still valid — and refreshes them in the `bulk` lane, 500 per
statement; a fanout that fails part way runs again from the start, and an
object enqueued twice folds into its one pending refresh. The newest
version can be expired while an older one still verifies, so the check covers
every version. An object with no valid version left has nothing to correct.
Metering therefore publishes tenant versions without re-signing a single object.
Every tenant compile that finds a valid tenant authority re-arms the objects
parked waiting for it, even when the compile publishes nothing. One input reaches objects without passing through
the tenant authority: process configuration comes from the tenant's tier. The
two Purser reasons that can change it, `subscription_authority_changed` and
`billing_tier_authority_changed`, refresh the tenant's objects directly.
Signing-key and tenant processing changes refresh all of the tenant's objects as
well. A playback-policy change does not: it refreshes the changed object and,
for a stream, the recordings, chapters and clips of that stream that cells may
still hold, since those are the only authorities that carry the stream's policy.

An object compile captures the current tenant version and, under that tenant's
current-pointer lock, publishes only if the tenant's `dependents_digest` is still
the one it captured; otherwise the compile is superseded and the fanout the change
enqueued recompiles it. Every field the object compile reads (lifecycle, billing
decision, preferred cluster, grants and their cells and entitlement fields,
placement policy and schema) is inside the digest, and the excluded allowances,
limits and decision text are read only by cells at decision time. A metering
publish therefore never supersedes an object compile. An object carrying
commercial quotes is the exception: it stays pinned to the whole tenant version
it was priced against and to that version's validity.

A live stream's signed origin, which is also its outage ingest owner, is the
cluster holding its ingest claim, else the cluster it last ingested on
(`streams.last_ingest_cluster_id`, kept after the claim is released) while the
tenant is still granted it, else the tenant's preferred cluster. The streams
trigger recompiles the stream only when that effective origin moves, so a
publisher that stops and reconnects to the same cluster publishes nothing, and a
move to another cluster publishes once. A publisher that disconnects keeps its
cluster as outage owner. Connected `ValidateStreamKey` and publish-intent
`ResolveStreamContext` report the same cluster when the caller names none, while
the route still offers it for live ingest, so local ingest promotion compares
like with like. A changed preferred cluster therefore does not move a stream that
has ingested before; the stream moves when its publisher ingests elsewhere. While
Commodore replicas of different versions run side by side, an older replica
compiles an idle stream's origin as the preferred cluster and a newer one as the
last ingest cluster, so an idle stream's authority can alternate between the two
until every replica runs the same version.

Push delivery is the fast path. Owner refresh and delivery have independent
worker loops, and delivery is per cell: the cells with a delivery waiting are
found by stepping through the per-cell index, and each is drained by its own
goroutine with its own workers, which the tick never waits for. A cell that is
slow or down keeps only its own workers busy. Each remote delivery
operation—including Foghorn resolution and apply—is bounded to 35 seconds, and a
cell's Foghorn addresses are remembered for thirty seconds and forgotten the
moment a delivery to it fails. Durable acknowledgement then gets an independent
five-second settlement budget, so a slow cell cannot spend the database
acknowledgement's deadline. Refresh compilation is bounded to 90 seconds per
claimed row and failure settlement likewise escapes the expired work context.
A delivery's lease spans one attempt (the five-second claim, the delivery, its
acknowledgement and failure record, plus five seconds), 55 seconds for an
ordinary delivery and 20 for a short lease, so a delivery whose replica stopped
mid-attempt is claimable again within a minute. Every claim (deliveries and
refresh obligations) runs in its own transaction bound to the claim's timeout: a
claim that times out or is interrupted before COMMIT rolls back, so a lease is
only ever held by a worker that received the row. Deliveries stay correct when a
lease lapses under a worker that is still running: a cell applies a version it
already holds as a duplicate and refuses an older one, and acknowledgement and
failure settle only a row still `delivering`.
Per-cell obligations lease and retry independently, and a newer version terminally
supersedes older pending work. A version past its validity is never sent and
leaves the queue.

On Foghorn registration/reconnect, Foghorn requests replay by its explicit control
cell ID, never by a virtual cluster ID, and says what it holds: a count and the
XOR of one SHA-256 per valid authority version. When that matches what Commodore
has on record as acknowledged by the cell, nothing is requeued, which is the
ordinary case; a cell that lost its database gets everything still valid again,
marked as a replay, and within a cell replayed deliveries are served after fresh
ones so catching up never holds back a change or a revocation. A cell that
refuses an envelope in a way retrying cannot change — it holds a newer version, a
conflicting digest, or a terminal tombstone, or the envelope is malformed for that
cell's release or past its validity on arrival — settles that delivery as
`rejected` instead of returning it to the queue; replay re-opens it. A failed
delivery backs off from one second, doubling, up to about three minutes. When a
cell's Foghorn answers the placement-capability read-back (its own report, or a
probe before publication), the cell is reachable again, and its deliveries
waiting out a backoff are made due at once, a thousand per statement.

Hourly reconciliation, retention and the activation-backlog sweep each cover the
whole catalog, so they run on one replica: the one holding the
`commodore-media-authority-sweeps` session advisory lock
(`pkg/database.SessionLeader`). Every replica ticks, asks for the lock first, and
skips the sweep while another replica holds it; a leader that stops or loses its
database session hands the lock to the next replica that asks. The minute sweeps
that settle undeliverable deliveries run on every replica, a batch of 500 per
statement until a batch comes back short, at most twenty batches a minute.

Hourly reconciliation is the safety net behind event-driven refresh. It gives any
live current authority without a renewal obligation one, recompiles the tenants in
use (a no-op unless content drifted) and any active tenant that was never
compiled, spread over the hour in the `bulk` lane, and re-arms parked targets. A
tenant nobody uses is left alone. Objects are recompiled only when the compiler
itself changed — its signing key or the shape of what it signs, recorded as a
fingerprint — because that is the one change no source event announces, and an
unchanged compile would never re-issue them. Source triggers in
Purser, Quartermaster, and Commodore ignore an UPDATE that rewrote a row with its
own values, and Quartermaster and Purser fold changes of one tenant and reason
into a single unfinished outbox row. A cell acknowledges only after signature, schema,
digest, audience, time, invariant, and monotonic-version checks pass and the
envelope plus decoded indexes commit to the Foghorn database.

An envelope valid for a minute or less is a short lease and has its own delivery
worker. `short_lease` is recorded on the delivery row when it is enqueued and
indexed with the cell and due time; deriving it from the version's validity at
claim time joins the queue to the whole version history on an expression no
index can serve. A short lease is renewed a third of the way through its
validity, so a renewal has at least twenty seconds to reach its cell. The worker
claims:

- as soon as the publishing transaction commits, on the replica that published,
  when it has a free delivery slot, and otherwise when a slot frees;
- at a failed delivery's next attempt time, on the replica that recorded the
  failure;
- on a fallback poll that finds everything else (an expired lease, work
  published by a replica that stopped). The poll starts at one second and backs
  off to four while claims come back empty or fail, with every wait jittered by
  up to a fifth, so idle replicas do not read the queue every second in step.

The claim has a two-second budget and reads the queue as sets, never with a
lookup per cell or per queued row: it reads the due short-lease deliveries of
current versions in one batched join and the cells with a delivery in flight in
one scan, takes the first claimable row of every other cell as its head, and
locks heads in cell order from where the replica's previous claim ended,
wrapping around, until the batch is full. Replicas claiming at once therefore
start at different cells. On YugabyteDB a lookup per cell or per queued row is a
storage round trip each, and a few hundred of them put the claim past its
budget. YugabyteDB logs
`Skip locking since entity was modified` whenever a claim passes over a head
another replica has just taken; that is the claim working, not a fault. A claim
that keeps failing is logged as a warning once and then at most once a minute,
and its recovery is logged once.

Queue indexes that order by `next_attempt_at` are explicitly range-sharded.
YugabyteDB otherwise makes the first index key a hash key, which turns a due
time lookup into a full-table scan even when only a few rows are pending.
Backlog metrics read the unsettled deliveries of each status through that
status's partial index before joining current and distribution state, and the
sweep that settles obsolete deliveries starts from the same index, so both cost
what the live queue holds; historical deliveries are never the driving side.
Each replica observes the queues once a minute, at a random point of its first
minute and jittered after that, with every statement bounded by a five-second
budget. The one-second authority workers start at a random point of their first
second and jitter every later wait the same way, and every claim and listing on
those ticks is bounded too.

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

The v0.3 upgrade preserves existing fingerprint rows, but a legacy row without
a node key is not local outage authority and cannot adopt a request-supplied
key through fingerprint lookup. Recover it with a fresh cluster-bound enrollment
token and an explicit identity-rotation request. Quartermaster validates the
stable machine or MAC binding before replacing the missing key, and peer-IP
fallback never seeds one. Foghorn persists local admission only after
Quartermaster accepts the signed registration, so the node needs that one
successful control-plane enrollment before it can reconnect during an outage.

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

| Path                                                       | Local decision and state                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                  |
| ---------------------------------------------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `PLAY_REWRITE`, HTTP `/play`, gRPC `ResolveViewerEndpoint` | Playback/internal identity, tenant decision, serving grants, and public/JWT/webhook policy come from the same verified object+tenant version. Artifact placement uses Foghorn inventory/database state without re-resolving catalog metadata. A viewer admitted by `PLAY_REWRITE` on that authority gives the serving edge a playback grant drawn from the same version and valid until the object and tenant hard validity; the edge answers the rest of that session's `PLAY_REWRITE` requests from it, and an authority apply pushes the changed grant to the edges holding it (see [edge playback grants](viewer-routing.md#edge-playback-grants)).                                                                   |
| `USER_NEW`                                                 | Rechecks the signed policy and authenticated node's local cluster binding before registering viewer/capacity side effects. A successful local JWT verification performs a bounded, non-blocking enqueue keyed by `(tenant, kid)`; one coalescing writer advances Foghorn's revision-fenced durable outbox off the viewer path. Commodore's `last_used_at` projection catches up after reconnect without becoming a playback dependency.                                                                                                                                                                                                                                                                                   |
| `PUSH_REWRITE` and ingest endpoint resolution              | Verifies the publishing-key digest and tenant authority locally. Endpoint resolution reads Commodore's live-claim lease once per request (one 1 s attempt); when Commodore is unreachable (unavailable, timed out or circuit open) it routes only to the pair's signed outage owner, refuses a stream without one, and answers further reads from that owner for 5 s without asking. `PUSH_REWRITE` takes the connected distributed claim in normal operation; on central failure, only the signed outage-owner cluster may admit a new publisher. Placement admission then decides on the claim `PUSH_REWRITE` took or the outage owner it verified, without another Commodore read. Existing durable sessions continue. |
| `STREAM_SOURCE` and `/source`                              | Uses signed pull/native/artifact identity and cell-opened secrets, then local/federated runtime placement. The caller's authenticated node cluster—not Foghorn's process cluster—identifies the destination.                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                              |
| `STREAM_PROCESS`                                           | Reads process JSON stamped onto the durable ingest session, processing job, or rolling DVR. It remains a one-shot Mist trigger; absence is a job/admission error, not a polling condition.                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                |
| Multistream/output status                                  | Admission retains the exact target payload and authority version as row-bound v2 ciphertext under `FOGHORN_STATE_ENCRYPTION_KEY`. Required status and placement updates are durable local outboxes and drain after reconnect. Push status upserts carry Mist event time, reject older observations, and prefer terminal status on a known-time tie. Legacy observations with no event-time header use arrival order instead of treating zero as a real shared timestamp. The credential-bearing payload is cleared when its source generation ends or is poisoned. During a rolling Foghorn upgrade, `_v2` states fence old workers.                                                                                      |
| Config restart                                             | Foghorn locks the per-node seed row, loads the latest durable payload, allocates the next `ConfigSeed` version, applies the producer's mutation/fallback merge, and persists that exact payload in one transaction. Helmsman persists/applies last-good state. An authenticated reconnect reporting a positive applied version keeps that local seed if Foghorn cannot persist a replacement. A fresh or unconfigured node is rejected and remains unroutable. Missing central components preserve only unresolved fields, while authoritative empty fields remove stale state.                                                                                                                                           |

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
otherwise they return unavailable. An unmarked schema-1 projection may shadow-compare against
connected behavior during rollout. Legacy connected responses do not carry a placement decision:
their equality cannot promote mixed-schema tenant/object pairs, and promotes a coherent schema-2
pair only on a replica that has installed placement enforcement. Push ingest, playback, pull
sources, managed inputs and artifact-source promotion all enforce this boundary. On an enforcing
replica a schema-2 authority is promoted at apply time instead: Commodore issued it only after the
cell attested enforcement in its `ApplyMediaAuthority` acknowledgements (see the activation
barrier in `media-placement-policy.md`). Even an empty schema-2 placement policy is not a legacy
default; a non-enforcing replica never promotes it.

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

## Staleness contract

A cell decides on local copies of state other services own. Each copy has one
rule for how long it is used and one rule for what happens while its source is
unreachable. Nothing on a viewer or publisher request path waits on Commodore,
Quartermaster or Purser while the local copy is usable.

| Data                                                                                        | Source and refresh                                                                                                                                                                                                                                                                                                                      | Used while                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                           | Source unreachable                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                              |
| ------------------------------------------------------------------------------------------- | --------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | -------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | --------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| Tenant authority                                                                            | Commodore delivery, or a cell's fetch. Renewal is published a third of the way through validity; past `refresh_after` (half) the cell fetches in the background.                                                                                                                                                                        | until `valid_until`, at most 24 h after issue                                                                                                                                                                                                                                                                                                                                                                                                                                                                        | decisions continue until hard expiry, then the cell asks Commodore and refuses                                                                                                                                                                                                                                                                                                                                                                                                                                                                                  |
| Media-object authority                                                                      | as tenant authority                                                                                                                                                                                                                                                                                                                     | until `valid_until`: up to 30 days when every target cell attests `long_validity_ready`, else 24 h (live) or 7 days (artifact) capped by the tenant; an object whose placement policy carries commercial quotes is also capped by its quotes, which Purser issues for 30 s, and by the tenant version it was priced against                                                                                                                                                                                          | as tenant authority; a quoted object hard-expires with its quote                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                |
| Placement pair cache                                                                        | Foghorn's own database, per process                                                                                                                                                                                                                                                                                                     | an entry answers for at most 1 s after the read began; it is dropped at once on a committed apply on this replica, on the cell's changelog announcement of another replica's apply, and when the restore-fence generation changes. Only stored rows are kept: freshness, restore-fence withholding and soft-expiry refresh are evaluated at each use. Absent, failed, hard-expired and changed-while-read reads are never kept. Concurrent reads of one pair share one database read; at most 65,536 pairs are kept. | local; correctness rests on the 1 s bound, not on the announcement                                                                                                                                                                                                                                                                                                                                                                                                                                                                                              |
| Placement membership (clusters of the cell, their active flag, their edge nodes' admission) | Quartermaster `WatchPlacementMembership` stream, woken when Quartermaster's membership fingerprint (read every 2 s while a watcher is connected) changes, plus `GetPlacementMembership` every 60 s; a dropped stream is reopened after 5 s. Stored in `foghorn.placement_membership_snapshot` on each new revision and loaded at start. | always the latest revision received or stored                                                                                                                                                                                                                                                                                                                                                                                                                                                                        | no hard expiry: Quartermaster is the only writer, so an outage freezes membership rather than making it wrong. `foghorn_placement_membership_snapshot_age_seconds` reports the time since the last confirmation and `FoghornPlacementMembershipUnconfirmed` fires once it stays above 120 s for a minute. A cell that has never received a snapshot, or a Quartermaster without the membership RPCs, falls back to a Quartermaster inventory read per request, one attempt bounded to 1 s behind the Quartermaster outage backoff.                              |
| Tenant cluster access                                                                       | the tenant authority's signed grants; placement takes the clusters of a cell from them, never from Quartermaster                                                                                                                                                                                                                        | as tenant authority                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                  | as tenant authority. Revoking access in Quartermaster writes a refresh outbox, so a cell stops using the cluster when the recompiled tenant authority is delivered (seconds); during a Commodore outage the revoked grant stays usable until the held tenant authority hard-expires (at most 24 h).                                                                                                                                                                                                                                                             |
| Node evidence                                                                               | Helmsman heartbeat, listener observation and metrics report                                                                                                                                                                                                                                                                             | each for 30 s from its stamp                                                                                                                                                                                                                                                                                                                                                                                                                                                                                         | n/a (node-local); a node whose evidence lapsed is not a placement candidate                                                                                                                                                                                                                                                                                                                                                                                                                                                                                     |
| Federation peer set                                                                         | Quartermaster `ListPeers` every 5 min, one attempt bounded to 5 s, plus the `WatchPeers` stream; the last answer is stored in the cell's Redis without expiry. The cell's leader holds PeerChannels to these peers and advertises the cell's streams to them                                                                            | always Quartermaster's last answer; a new leader starts its term from the stored answer                                                                                                                                                                                                                                                                                                                                                                                                                              | no expiry: Quartermaster is the only writer, so an outage freezes the set and the leader keeps its PeerChannels and advertisements. A peer is removed only when Quartermaster answers without it. A peer cluster Quartermaster still names with shared tenants but without a healthy Foghorn (every replica restarting) keeps its last known addresses, so the leader redials them as soon as they serve again.                                                                                                                                                 |
| Livepeer gateway assignments                                                                | Quartermaster `DiscoverServices` per candidate cluster (origin, official, the process's own), held per process for 60 s, or 5 s when the cluster has none. A refresh is one attempt bounded to 1 s; `PUSH_REWRITE` refreshes only while more than 1 s of its 3 s budget remains, which the session mint and projection keep             | a fresh answer for its window; past it, until a refresh answers                                                                                                                                                                                                                                                                                                                                                                                                                                                      | the last answer is used whatever its age, and a cluster never answered falls back to local MistProcAV. A refresh that gets no answer ends discovery for the rest of that resolution, and an unreachable Quartermaster opens a process-wide 5 s backoff (one Unavailable, or three consecutive timeouts) in which no request-path Quartermaster call is made.                                                                                                                                                                                                    |
| Node attribution                                                                            | the node's authenticated registration in node state; Quartermaster `GetNodeByLogicalName` only for a node without one, cached for 1 h                                                                                                                                                                                                   | as node state                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                        | the lookup is one attempt bounded to 1 s behind the Quartermaster outage backoff; an unattributed node is refused, not guessed                                                                                                                                                                                                                                                                                                                                                                                                                                  |
| Remote cell observation                                                                     | the peer cell's Foghorn                                                                                                                                                                                                                                                                                                                 | a complete observation is reused for 1 s and refreshed in the background once it is 0.5 s old; at most 8,192 observations and 512 concurrent reads per process                                                                                                                                                                                                                                                                                                                                                       | a failed or stale observation contributes no candidates and marks the decision incomplete; an incomplete or failed observation is never kept for reuse                                                                                                                                                                                                                                                                                                                                                                                                          |
| Ingest ownership                                                                            | Commodore's live-ingest claim: a 30 s lease renewed every 10 s by the replica that owns the publisher's node, so three attempts per lease                                                                                                                                                                                               | `PUSH_REWRITE` placement decides on the claim it took; endpoint resolution makes one connected read per request, bounded to 1 s                                                                                                                                                                                                                                                                                                                                                                                      | the pair's signed outage owner (`outage_ingest_cluster_id`), and none means refuse. `PUSH_REWRITE`'s stream-key validation and claim, and the ingest front door's (HTTP and gRPC) stream lookup, are each one attempt bounded to 1 s that does not wait for the connection, so an unreachable Commodore leaves most of the 3 s admission budget to the outage owner path. An unreachable Commodore opens a process-wide 5 s backoff, shared by those calls and the endpoint read, in which they use the signed local authority and outage owner without asking. |
| Cell placement capability                                                                   | each replica heartbeats `foghorn.control_replicas` every 15 s; replicas seen within 60 s are live                                                                                                                                                                                                                                       | reported to Commodore after the beat that changes it, when Commodore (re)connects and at least every 5 min; a failed report is retried after 2 s                                                                                                                                                                                                                                                                                                                                                                     | Commodore keeps the attestation it recorded: an acknowledgement or report without one changes nothing, and only live replicas attesting that they do not enforce withdraw readiness. An unchanged attestation rewrites the row at most every 5 min.                                                                                                                                                                                                                                                                                                             |
| Refresh obligation claim (Commodore)                                                        | event and bulk refresh compiles                                                                                                                                                                                                                                                                                                         | the claim's 10 s lease, renewed every third of it while the compile runs                                                                                                                                                                                                                                                                                                                                                                                                                                             | a stopped holder releases the target when its lease lapses                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                      |
| Authority delivery claim (Commodore)                                                        | ordinary deliveries to a cell                                                                                                                                                                                                                                                                                                           | a 55 s lease: the claim (5 s), the delivery (35 s), recording its acknowledgement or failure (2 x 5 s) and a 5 s margin; deadline deliveries hold 20 s                                                                                                                                                                                                                                                                                                                                                               | a stopped holder's delivery is retried when its lease lapses                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                    |

## Validity and outages

What authority costs follows what is in use, not the size of the catalog. An
authority is in use while it was decided on in some cell within the last thirty
days, was created within the last seven, or (a live stream) is being ingested.
Cells report the authorities they admit something on, once per authority per
day; Commodore keeps that to the day in `media_authority_use`, and every use of
an object is also a use of its tenant, so a tenant is never colder than its
objects. An authority with no recorded use counts as used when recording
started, so nothing is mistaken for unused by an upgrade.

An authority in use is published, delivered to the tenant's cells, and renewed.
One that is not is not renewed for its own sake: its renewal goes `dormant`, and
the copies cells hold run out. While a copy may still be valid somewhere, a
change is still published, valid until the longest-lived copy runs out and
never longer, so correcting a copy does not keep an unused object in cells. It
is shorter when the compiled authority allows less, such as a new thirty-second
quote or a shortened grant, because the signer refuses a version that outlives
those. "May still be valid" is not decided by the current version alone: a
shorter-lived replacement can run out while a cell that never received it still
holds the longer version it replaced. A cell holds at least the version it last
acknowledged (its fence refuses anything older) and at most the last one it was
sent, so every version in that range, for every cell, counts. So while the
current version runs out before an older copy does, it is renewed when due,
unchanged or already expired: delivery never sends an expired version, and only
a newer valid one reaches the cell still holding the older. Each renewal is
bounded by that older copy, and the loop ends as soon as every cell has
acknowledged the correction, or at the latest when the older copy runs out.
Once no version is valid there is nothing left to correct and nothing is
published; a change is compiled the next time the authority is used. A
tombstone is published regardless, and is renewed by the same rule: an expired
tombstone cannot be resent either, and a cell that missed it still holds the
object.

A change to an object is never parked behind a tenant authority that has run
out while a cell may still hold a valid copy of the object. It is compiled on
that lapsed tenant version (a placement object captures it as its parent all
the same; a quoted one is still bounded by its quote) and published at once.
Publishing first is not enough on its own: deliveries to a cell run in
parallel, and another object's fetch can bring the tenant too, so the renewed
tenant can reach the cell before the correction does. The cell closes that
gap. A tenant authority that brings back a tenant the cell held only past its
validity records `objects_trusted_from` on its projection. First delivery to a
cell is not revival and does not introduce a fetch dependency. Every object
copy lacking current-version confirmation begun after a revival barrier
reads hard-expired. `confirmed_at` records the confirmation's start using the cell
database clock; delivery arrival and duplicate delivery never advance it.
A fetch that revives the tenant and supplies its objects uses the same start
instant for both. Split object/tenant reads also capture and compare the parent
version. A parent change causes one bounded local re-read of the object; a
revival barrier or changed object still fails closed. A renewal that never lapsed
withholds nothing in a coherent pair.

**Restore fence.** Bounding corrections by acknowledgements relies on a cell
never holding less than it acknowledged, and a restored cell database breaks
that. Such a cell can hold a replaced version that is still valid, while its
replacement has expired and cannot be resent. Two things keep it from being
decided on:

- The control plane, when a cell's per-identity inventory proves it holds less
  than an acknowledged version,
  stops trusting that cell's acknowledgements
  (`media_authority_cell_ack_resets`). Every version the cell was ever sent
  then counts again. It issues again every authority whose current version ran
  out while an older one may still be valid there, and resends the current
  versions that are still valid.
- The cell withholds its authority behind a fence. While the fence is up, an
  authority is decided on only after current-version confirmation begun in that fence
  generation confirms it (a new version or one already held). An ordinary
  delayed delivery cannot confirm it. Until then a read reports it
  hard-expired, which every decision path answers by asking the control plane
  and, when that cannot be done, refusing. The durable fence
  (`foghorn.media_authority_restore_fence`) is raised by
  the CLI restore workflow and by a proven inventory regression.
  It is lowered only after a matching acknowledged-set summary, or a completed
  inventory confirming the surviving copies current, taken after the fence, and
  while it is up the cell summarises again every thirty seconds. Every process
  loads the durable marker before serving. An ordinary restart uses valid local
  authority immediately, including during a core transport or database outage.
  A restore must stop every replica and fence the restored database before any
  replica restarts; the CLI `cluster restore-fence prepare/complete` workflow
  enforces these steps for native PostgreSQL/Yugabyte restores.

The digest compares the highest acknowledged version, not the latest published
version. A mismatch requests ordered inventory pages of at most 500 identities;
it does not reset acknowledgements or replay the catalog. Pages compare each
identity independently, so unrelated pending or backed-off deliveries cannot
hide a regression. Applied-but-unacknowledged current copies can be confirmed
immediately. ACK races remain inconclusive and are checked again. Their ordering
uses a core-database clock watermark received before the cell reads the page,
not a comparison between the two database clocks.

Inventory confirms current held versions in batches without compiling each
object. Each invocation handles at most four pages and retains its cursor for
the next invocation, resumed after one second; failed or inconclusive passes
retry after thirty seconds. Byte-ordered indexes support keyset paging without
sorting the full catalog on each page. Ordinary delivery cannot provide this freshness proof;
delivery of a still-withheld active object returns a retryable confirmation
requirement, not a usable ACK. A valid current tenant/object pair can also be
fetched without recompilation. Missing or expired current pairs are compiled
under the normal fences and deadlines.

Core and cells require recovery protocol 1. Deploy Commodore before Foghorn
within the coordinated release; there is no legacy full-catalog replay fallback.
An unsupported response preserves any existing durable fence, logs an upgrade
requirement, and retries every thirty seconds on the same connection. Missing timestamps and clock skew over
five minutes fail explicitly without mutating acknowledgements or delivery.
Reachable but inconclusive responses neither raise nor lower a trust fence.
Only a checked match can lower a durable fence. The coordinated provisioning
plan orders Commodore before Foghorn; v0.3.11 prohibits Commodore rollback.

A managed-stream object whose parent is missing makes the local set incomplete,
requests the pair, and permits connected recovery. Incomplete local sets never
authorize retraction of the cluster's other managed streams.

Use wins every race with dormancy. Recording use and reviving the renewal commit
together, and the renewal is revived even when the use was already recorded that
day. A renewal compile in flight is folded (its revision moves), so a compile
that has just decided the authority is unused cannot settle it dormant
afterwards. A compile recording use of the authority it is compiling does not
fold its own lane. A dormant renewal whose authority was used within the use
window is counted as expired-in-use, so a lost revival still pages; that count
starts from the recently used set, so it never reads the authorities nobody
uses. There is no message that
removes an authority from a cell: the control plane stops renewing and the copy
expires, so a change signal only ever advances what a cell holds.

A decision that needs an authority the cell does not hold as valid asks for it:
`FetchMediaAuthority` records the use, compiles the tenant if its current
version has lapsed (the object is built on that version; an older one a cell
may still hold does not help), compiles the object, and returns the envelopes signed for that cell, which
applies them and decides locally. This is what serves an object nobody has used
for a while, and it is the only way one can be served where placement is
enforced, because final placement admission decides on the local tenant and
object pair alone. The placement pair reader asks too, so federation discovery,
admission and ingest resolution on any cell a request reaches repair that cell's
own authority rather than waiting for delivery. A decision waits at most two
seconds for it, and less when its own deadline ends first; the fetch then
carries on for any other decision waiting on it. A placement read allows each
local read one second and the fetch its two, so a slow database never eats the
fetch's time. A name that does
not exist is asked for once per thirty seconds; and after any other failure
decisions stop waiting for five seconds, so an unreachable control plane never
adds a wait to every decision. When the fetch cannot be made the read stands as
it was: absent falls to connected validation, expired is refused. The cost of
all this is one sentence: **an object nobody has used for a month cannot start
during a control-plane outage.** Everything in use can.

A cell forgets an authority that ran out once no older signed version of it can
still verify, which is `MaxMediaObjectValidity` after it was issued. That bound
is enforced on every envelope at signing and at verification, and it is why
forgetting the version the cell had reached cannot let an older one back in. A
tombstone is never forgotten. A forgotten object is, to that cell, one it never
held: readiness markers and the placement revision fence start over, and the
stale-pointer purge sweeps its pointers and thumbnails.

Tenant envelopes hard-expire no later than 24 hours. Media-object envelopes
hard-expire no later than thirty days and are not capped by their tenant's
validity: every decision a cell makes also requires the tenant authority, so what
a cell serves while it cannot reach the control plane is still bounded by the
tenant's 24 hours, and suspension, grant changes and billing travel in the tenant
authority. A version carries a nominal validity with optional shorter per-cell
correction caps. A replica that predates the longer bound rejects the envelope outright, so it is issued only
when every cell the version goes to attests `long_validity_ready`; until then
objects keep the earlier bounds (24 hours live, seven days artifact, capped by
the tenant). Every object is compiled against the tenant authority
it captured and is refused if that tenant changed before publication; it is
bounded by the tenant's validity only when its placement policy carries
commercial quotes, and then by the quotes as well.

Every published version of an authority in use schedules its own renewal a third
of the way through its validity, and `refresh_after` sits at half. Every
authority, tenants included, renews at a fixed offset of its own, so authorities
published together (a startup, a re-issue, a restored database) do not renew
together for as long as they live. Renewal is a new version, so it changes nothing
in how a cell applies authority. A cell that still holds a version past
`refresh_after` is looking at an overdue renewal with half the validity left to
recover in: it goes on deciding locally and asks for that one authority in the
background.

A change is never waiting for renewal: source changes publish through the event
lane as they happen. The revocation bound is therefore the time from an owner
mutation to replacement delivery, capped by the old envelope's hard expiry, and
the renewal cadence does not enter it. Renewing more often than validity requires
adds versions, deliveries, and audit rows in proportion to catalog size and time,
and buys no tighter bound.

At hard expiry the cell asks Commodore, and refuses if it cannot. Signed denials
and tombstones remain denials. A change is delivered to the cells that may still
hold a valid copy, including ones the tenant no longer has a grant in, which
prevents a removed cell from retaining newly valid secrets; a cell whose every
copy has run out has nothing left to correct and drops out. A tombstone goes to
historical targets still within their fixed correction horizon. A hard-deleted
tenant is compiled from its last version into a tenant tombstone and delivered
to that same retained target set; it is not retried forever as an unresolved owner
lookup. A tombstone is terminal and renews only while correcting an older valid
copy. Pending delivery, version lag, apply
rejection, parked refresh targets, and local freshness are operational signals,
not permission defaults.

Rejected-delivery alerts and the CLI doctor use the same actionable-rejection
view. They resolve once neither the current version nor any older copy the cell
could hold remains valid. An expired short correction still alerts while its
older long-lived copy remains usable. Rejection history is retained.
The signed-apply counter describes verification and persistence, not a later
restream-reconciliation retry. A committed apply awaiting trust confirmation
remains applied; an unacknowledged delivery remains visible in backlog metrics.

New x402 settlement and other new control-plane mutations remain explicitly
online-only. Bootstrap, node adoption, catalog management, and creation of a new
processing job are management paths, not outage media-serving paths.

### Known limitations

- **An object nobody has used for a month cannot start during a control-plane
  outage.** Its authority is not kept in cells, and the fetch that would bring it
  back needs Commodore. Thirty days is measured from the last decision any cell
  reported on it; an object that is ingesting, or younger than seven days, is in
  use regardless.
- **An object whose placement policy carries commercial quotes depends on
  Commodore and Purser within about 30 seconds.** Its authority is capped by
  its quotes, which Purser issues for 30 seconds, so a cell holds a valid copy
  only while Commodore can recompile it with a fresh quote. During a Commodore
  or Purser outage such an object hard-expires and is refused once its last
  quote runs out; objects without quotes keep their full validity.
- **Quartermaster access revocation takes effect at authority delivery.**
  Placement reads a tenant's clusters from the signed grants in its tenant
  authority, so revoking a tenant's access in Quartermaster reaches a cell when
  Commodore delivers the recompiled tenant authority, normally within seconds.
  While Commodore is unreachable the cell keeps using the grant it holds until
  that tenant authority hard-expires, at most 24 hours later.
- **A restore bypassing the CLI fencing workflow is unsafe.** A process cannot
  distinguish an unfenced backup from an ordinary restart without depending on
  core availability. Always stop every replica with `cluster restore-fence
prepare`, perform the native restore, then use `cluster restore-fence complete`
  to fence the restored databases before replicas resume. Background inventory
  detects acknowledged regressions but is not a substitute for this boundary.
- **An always-on managed stream lost to a renewal failure can be retracted.** The
  managed-stream set is reconciled from local rows once it is complete. Always-on
  streams report use for as long as they are configured, so they are not
  forgotten as unused, and `MediaAuthorityExpiredWarm` pages within minutes of
  one expiring; a cell that forgot one thirty days later would drop it from the
  set.
- **A fixed grant expiry ends tenant validity until the next compile.** Tenant
  validity is capped by the earliest grant expiry, and compiling against an
  already expired grant is a transient failure. Foghorn does not enforce grant
  expiry per grant, so a tenant whose grant lapses hard-expires as a whole until
  the owner's change recompiles it.
- **Asymmetric partitions can admit two publishers.** The signed outage owner
  is deterministic, but it cannot observe a still-live claim isolated in
  another cell. If that cell can keep serving while the outage-owner cell loses
  the control plane, both may temporarily accept ingest until the partition or
  the remote claim/lease converges. The authority model bounds and identifies
  the fallback owner; it is not a cross-partition consensus protocol.
- **A lost liveness signal eventually releases authority.** The cross-cell
  active-ingest placement claim is a 30-second lease that Foghorn renews every
  10 seconds for every unended PostgreSQL ingest session whose node's control
  connection that replica owns, including a pending generation whose blocking
  admission is still being confirmed. An unreachable node's claims therefore
  lapse, which is what lets a publisher that moved to another node be
  admitted; the unreachable node's session itself stays open. Within a cell,
  publisher identity and tenant stream capacity come from PostgreSQL ingest-session rows; Valkey expiry cannot revoke a
  publisher. `PUSH_INPUT_CLOSE` is the exact PID/generation finalizer.
  Event-time-fenced `STREAM_END`, takeover by a publisher admitted on another
  node while this node is absent, the node's re-registration inventory,
  projection abort, placement-claim loss, and the 5-minute lost-node window
  (`node_lost`: no control connection and no live pulled copy of the node's
  streams on a connected edge) are backstops that end only the matching durable
  generation. Shorter control-connection loss ends none. Only the close gives
  its claim back at once; the other backstops leave it to lapse. A publisher
  admitted inside that window is refused `DUPLICATE_INGEST` with the owning
  claim token when the claim is in its own cluster; if every session under that
  token has ended in this cell's database, the admission releases exactly that
  token (owner-fenced at Commodore) and claims once more. A live, unknown or
  other-cluster owner stays a duplicate. Helmsman's Mist inventory supplies the local runtime backstop:
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

`frameworks cluster doctor` reports **Media authority convergence**: unhealthy
when a refresh target is parked, a refresh or a current delivery has been stuck
for more than five minutes, an actionable rejection remains, an in-use authority
is hard-expired, or authority garbage collection is overdue. Expired dormant
rows alone are not an incident. `frameworks cluster diagnose media-authority` gives the
detail behind it — obligations by lane and status, parked targets with their
reason, versions per authority per hour and how many re-signs were identical,
deliveries per cell, and each cell's recent rejections next to the version it
holds. Both run read-only SQL on the database host, so they cover every Foghorn
cell database. The matching alerts are `MediaAuthorityRefreshStuck`,
`MediaAuthorityRefreshParked`, `MediaAuthorityRejectedDelivery`,
`MediaAuthorityVersionChurn`, and the Purser and Quartermaster outbox alerts.

The v0.3.11 rollout needs no operator step. Expand adds the obligations table,
the digests, the `rejected` delivery status, and `short_lease`; an old Commodore
replica keeps draining the refresh inbox and never touches the new table. Once
the new binary runs, it folds the inbox's unfinished rows into one obligation per
target and republishes every authority once, because versions issued before the
digests existed have none to compare. Quartermaster's folding enqueue function is
installed in postdeploy, after its unique index is verified, so its `ON CONFLICT`
never runs without a valid arbiter.

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
Ingest endpoint discovery deliberately reads Commodore's ownership lease once
per request, bounded to one second, so a live claim can override the signed
outage owner. When that read finds Commodore unreachable, the request routes to
the signed outage owner, and reads in the following five seconds use it without
an attempt. `PUSH_REWRITE` placement admission makes no ownership read: it
decides on the claim the trigger took or the outage owner it verified. Neither
path calls Quartermaster or Purser.

Foghorn retains apply/verification audit observations for 30 days. Each prune
tick drains up to sixteen bounded batches, because one row is written per apply
attempt and a single batch per tick deletes fewer rows than a small cell writes.
Every rejected apply is also logged with the incoming and held versions. This
diagnostic retention never changes the signed current authority or its decision.

Refresh obligations are not retained work: the table holds one row per target and
lane and stays the size of the catalog. The refresh inbox that preceded it is
still present; Commodore folds its unfinished rows into obligations in batches
and keeps deleting its completed rows after seven days. Commodore retains expired
central authority history for thirty days after `valid_until`. Hourly bounded
batches delete only acknowledged or superseded deliveries for a non-current
version, then remove versions with no remaining delivery. Current versions and
pending or delivering obligations are never eligible, even when old. Targets
and acknowledged-distribution high-water marks remain as the durable record of
which cells must receive future revocations and how far each cell converged.

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

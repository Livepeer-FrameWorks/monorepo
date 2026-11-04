# Publisher admission

Ordinary push ingest uses a signed configuration snapshot, a separate global
ownership lease, and two atomic cell database transitions. This keeps database
branching beside the data when a Foghorn's database is across a WAN. It does not
require regional database provisioning. Regional durability remains a separate
[topology choice](multiregion.md).

## Ready admission

The production coordinator is `triggers.Processor.ProcessPublisher`. Both unary
and legacy control transports call it. Atomic transitions activate only for a
ready schema-4 pair with complete publisher configuration and an edge that
supports durable settlement. Unary callers carry that contract; legacy messages
advertise it with the valid publisher deadline added alongside settlement WAL.
Older or malformed legacy messages retain synchronous denial cleanup, including
when the cell already holds schema-4 authority. Before that barrier,
cold discovery and mixed cells retain the compatible Go mint/projection path
and its synchronous cleanup. A cell cannot create a new atomic generation
while an older live replica could own its renewal. The ready schema-4 path makes three Foghorn SQL
calls and one Commodore lease RPC, with a Valkey source CAS between preparation
and confirmation:

| Step                             | Responsibility                                                                                                                                                                                                                                                          |
| -------------------------------- | ----------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `GetPublisherAuthorityPreflight` | Read the coherent tenant/object pair by credential digest and find this node/execution's existing session cluster in one call. Seed the existing placement-pair cache with the verified pair.                                                                           |
| `AcquirePublisherLease`          | Check Commodore's current published versions, payload digests, cell audience, lifecycle, billing, owner activity and destination grant; acquire or refresh the exact execution's 30-second lease.                                                                       |
| `PreparePublisherAdmission`      | Under execution → tenant → stream locks, check abandonment, idempotency, takeover, close tombstones and tenant capacity; insert or resume a pending generation and allocate its source revision. Persist connected/idle and DVR-stop obligations in the same operation. |
| Source CAS                       | Project only that generation and revision through the existing Valkey fence.                                                                                                                                                                                            |
| `ConfirmPublisherAdmission`      | Recheck the unended generation, execution marker, source revision and decision expiry; mark active and atomically persist protected output and admission-effect obligations.                                                                                            |

The SQL routines are versioned, VOLATILE, invoker-security functions with a fixed
search path. They contain no external I/O or commits. Each generated call is one
implicit transaction; the application consumes its result before treating the
operation as committed. Go generates event/generation identities once and
reuses them on database retries. Tenant capacity reads happen after the tenant
lock, including when the caller waited behind another publisher.

A signed permission is reusable configuration, not exclusive ownership. The
ready path does not call `ValidateStreamKey`, Purser, or Quartermaster to rebuild
configuration. Commodore checks its own current publication; it does not accept
caller-supplied configuration as permission. A stale publication returns a
refresh outcome, starts bounded shared filling, and refuses this attempt.
Denials remain denials. Capable edges' cold validating claims also carry node/cell/execution
identity, serialize against the same global abandonment fence, and require an
explicit server acknowledgement of fencing support. Outage admission retains the signed deterministic owner
and the existing partition limitations in [media authority](media-authority.md).

Identity, process/DVR settings, opened output configuration, limits and placement
versions come from the same pair. Destination-specific grants and attribution
use the publishing node's actual cluster, which can differ from the historical
origin/outage owner. Placement must decide on those exact versions before
preparation, and preparation and confirmation check the decision's expiry.
Invalidation epochs prevent an older preflight read from repopulating a pair
cache after a newer local apply. Missing publisher configuration cannot hide a
signed tenant/object denial.

Exceptional takeover, stale-owner recovery and divergent source repair may
perform more operations. They retain exact-generation fences and are not the
normal latency bound. Unknown, live or other-cluster lease owners remain
exclusive; a duplicate is released only with durable proof that its owner ended.

DVR stop obligations commit with preparation/settlement. Bounded, nonblocking
hints wake the existing DVR recovery worker immediately after commit; network
sends occur on that worker. Lost hints or a crashed process retain the durable
periodic recovery path.

## Request lifetime and shared work

Helmsman starts the request lifetime before its causal queue. The shared
`mist.PublisherResponseTimeout` is four seconds, the edge's useful response
window. Foghorn receives the remaining unary deadline and cancellation; it does
not start an independent three-second background operation. Legacy stream
messages carry the publisher deadline and enter the same coordinator. Admission
has at most 64 active and 128 waiting requests per replica; waiting consumes the
caller's lifetime. This resource bound is separate from publisher capacity.

After node-key registration, Foghorn issues a random, connection-bound unary
capability over the authenticated control stream. `AdmitPublisher` requires
that capability and a deadline, overwrites caller node/cluster/cell attribution,
and cancels on control-stream replacement or loss. The unary call uses the same
pinned endpoint/channel as `Connect`; it is not independently round-robin routed
to a replica that does not own the node. A failed unary request is not replayed
through the legacy transport with an ambiguous outcome.

A configuration fill belongs to the cell, rather than one publisher. Concurrent
misses join one fill. Each caller waits at most two seconds within its own
remaining lifetime; the shared compile/fetch/apply may continue for up to 30
seconds with at most 64 concurrent fills. The bound covers Commodore's existing
compile lifetime and WAN apply work. A timed-out first waiter therefore does
not discard a bundle needed by the next attempt. A complete, ready, unexpired
pair is still required before granting access. Existing refresh/backoff and
negative-cache rules apply.

Deadlines bound useful work and resource retention. Authority expiry, request
lifetime, lease expiry, retry attempt limits and recovery retention have separate
correctness roles. See [gRPC deadlines](https://grpc.io/docs/guides/deadlines/)
and [cancellation](https://grpc.io/docs/guides/cancellation/). Lost acknowledgements
can hide committed side effects; [AWS's retry guidance](https://aws.amazon.com/builders-library/timeouts-retries-and-backoff-with-jitter/)
explains why retrying needs an idempotency identity rather than an assumed rollback.

## Unknown results and settlement

The idempotency identity is node plus Mist execution UUID; a generation and its
source revision are durable. A lost prepare/confirm reply is an unknown result.
A retry reads that execution and resumes its generation rather than allocating
another. Even an active resume confirms its current fence before returning
accept, because abandonment may have won after preflight. Durable effects are
not replayed by an active resume.

Helmsman's existing trigger WAL owns the final response outcome. It persists
an accepted generation before returning acceptance to Mist. A definitive
refusal, or an execution whose entire Mist delivery lifetime ends without an
accepted response, creates a durable abandonment obligation. Foghorn verifies any existing execution's connector PID before recording
the node/execution marker and acknowledging it, so a malformed duplicate cannot
retire an accepted execution. Every definitive forwarded domain denial reports
abandonment, unless that same execution's accept already reached Mist. Late preparation and
confirmation cannot admit it. A request timeout alone does not synchronously
retire a possibly committed generation.

Foghorn's recovery job settles abandoned generations and pending generations
past `mist.BlockingTriggerLifetime`. Atomic settlement checks the current row
under the same execution/stream fences, advances the offline revision and
persists idle/DVR-stop obligations. A lapsed scan cannot retire a generation
that became active before settlement acquired its lock.

Legacy generations use their legacy retirement transition. Their abandonment
marker remains pending until the exact metadata-free tenant/stream/cluster/token
lease is released, including retries after the session has already ended.
Recovery keeps local session completion (`publisher_sessions_settled_at`) separate
from global lease forwarding. A bounded marker queue is selected before looking
up its sessions; executions that never minted also finish this local phase. A
marker completes locally only when no open session remains, while the marker
itself continues to prevent a late mint. Central outages cannot keep completed
local work in the scan. Expired pending sessions use their own partial index.
Retention requires both phases to finish, within the existing two-day hard cap.

Credential lookup includes inactive/tombstone authorities, so its digest index
covers those lifecycles. Legacy cleanup and connector-identity checks share an execution index that covers
ended rows and both protocol versions. The next release includes online index creation and readiness checks;
startup capability verification also requires both settlement-phase columns.

The job also forwards every new abandonment marker to Commodore, including
executions with no local session. `AbandonPublisherLease` and acquisition share
a cell/node/execution lock: abandonment releases only that exact lease and
prevents a later acquire of that execution. This covers a committed lease whose
reply was lost before cell preparation. Lost abandonment acknowledgements leave
the forwarding obligation pending for retry. Ended generations release leases
in an owner-fenced batch; acknowledgement is persisted in a tenant-scoped batch.
An explicit terminal-fence acknowledgement settles the requested execution even
when another owner prevents clearing the current lease. Missing acknowledgement
or an unknown RPC result retains the obligation. Strict and compatibility renewal
groups each receive a share of their existing batch lifetime, so one contract's
failure cannot skip renewal of the other group.
Recovery runs on the current cell leader once per second with a five-second pass lifetime, batches of 32
settlements/markers, and at most eight parallel operations.

`RenewPublisherLeases` requires an existing matching owner, even if a caller
omits the compatibility flag. A queued renewal cannot restore a released owner.
The renewal job separates these owners from legacy/outage owners that use the
claim-establishing contract, sharing the batch's deadline across both calls. New
renewals carry node/cell identity and acquire sorted execution locks before
checking terminal markers, so an outage publisher may acquire its first lease
after reconnect without resurrecting an ended execution. Execution-aware close
and recovery releases record terminal markers in that same transaction before
clearing ownership. SDKs require acknowledgement of these fences for claims,
renewals and releases; older metadata-free clients retain compatibility behavior
before the schema-4 cutover. Global
abandonment markers are kept for two days, beyond request/fill lifetimes. Cell
markers use ordinary retention after global acknowledgement, with a two-day
bound if Commodore stays unreachable.

## Compatibility and rollout

The v0.3.14 expand migrations add cell transition routines/recovery columns and
Commodore's node-bound lease fields and execution markers. Runtime capability
checks require these before the new binaries serve requests. No new secrets,
GitOps values, or database relocation are required.

1. Apply expand migrations through the normal release workflow.
2. Upgrade every Commodore replica before activating new Foghorn capabilities.
   All replicas must implement lease acquire, strict renew and abandonment.
3. Upgrade Foghorn replicas in each cell. Publisher-ready capability is advertised
   only after every live replica reports schema 4. Schema 4 includes complete
   publisher configuration and DVR chapter settings. Older capability lists
   remain unchanged, so older Commodores ignore the additive readiness flag.
4. Observe schema-4 publication and local readiness. The new warm path also
   requires a capable Helmsman. A mixed cell uses the older configuration path. A schema-4 caller
   never falls back from an unimplemented lease RPC to legacy validating mutation.
5. Upgrade Helmsman. Each new authenticated control connection receives a unary
   capability. Older Helmsmen retain compatible Go transitions and synchronous
   denial cleanup through the same coordinator.

Old decoders reject unknown signed payload fields; schema activation is a
compatibility barrier, not merely a preference. The release catalog disables
rollback for Commodore and Foghorn after this cutover. Recovery must retain
strict renewal and schema-4 support; do not roll either service back alone.
Use the normal release plan/dry-run/apply workflow from the
[operator release documentation](../../website_docs/src/content/docs/operators/release-lifecycle.mdx).

## Verification and diagnosis

`make verify-publisher-admission` runs ownership, capacity, idempotency,
confirmation-loss, expiry, abandonment and settlement races on PostgreSQL and
Yugabyte. `make verify-ingest-admission-wan` injects 110 and 150 ms RTT through
all physical dials of the production Yugabyte pgx exec-mode driver, with warm
pooled sockets. It checks the three SQL calls, one lease and zero configuration
validation calls; the placement test adapter isolates this coordinator test.
Its small latency sample is regression evidence, not a production SLO.

`make verify-publisher-recovery` checks bounded work with active sessions and a
retained marker backlog. `make verify-publisher-lookups` covers inactive signed
credentials and ended legacy executions. `make verify-publisher-schema` verifies
latest-tag upgrade convergence, capabilities and interrupted-upgrade completion
on PostgreSQL and Yugabyte. The plan auditor runs the JSON transitions with
verified predecessor and result state inside a rolled-back transaction.

Stack scenario 53 injects 110 ms dependency delay into both B Foghorn replicas,
then uses a single real RTMP publisher invocation per attempt. It checks the
confirmed strict-owner generation, one admission call per encoder, playable TS
through both replicas, durable release and a reconnect. Full stack scenarios cover the real placement gate,
edge WAL, source lifecycle and durable effects. Run the stack on remotedev;
production/staging verification is a separately requested rollout action.

Use tenant-scoped `frameworks cluster diagnose media --stream-id <UUID>
--tenant-id <UUID>` with the existing manifest/context flags. It reports bundle
versions/readiness, exact generations, source revisions, pending release and
abandonment markers from each service's own database without exporting payloads
or publishing credentials. The bounded-phase histogram is documented in
[metrics](../standards/metrics.md).

Database ordering must be proved on the pinned engines. See
[Yugabyte concurrency control](https://docs.yugabyte.com/stable/architecture/transactions/concurrency-control/)
and [PostgreSQL data-modifying CTE semantics](https://www.postgresql.org/docs/current/queries-with.html#QUERIES-WITH-MODIFYING).
The routines use ordered statements after locks; an unordered data-modifying
CTE is not a substitute for those visibility and capacity guarantees.

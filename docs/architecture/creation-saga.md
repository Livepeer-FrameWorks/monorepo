# Artifact Creation Saga (command ledger + intent + ack)

How a clip / DVR / VOD create stays consistent across Commodore (control plane) and
Foghorn (media plane) despite crashes and lost/ambiguous responses. This is the
canonical description; the code carries only concise local invariants that point here.

## The three durable records

| Record              | Owner     | Table                                                      | Written                                                             |
| ------------------- | --------- | ---------------------------------------------------------- | ------------------------------------------------------------------- |
| Creation **intent** | Commodore | `commodore.artifact_creation_intents`                      | BEFORE the cross-service Foghorn create                             |
| Command **ledger**  | Foghorn   | `foghorn.artifacts` + `foghorn.artifact_creation_commands` | during the create RPC (`accepted` → `committed`/`rejected`)         |
| Business row        | Commodore | `commodore.clips` / `dvr_recordings` / `vod_assets`        | clip: on commit; dvr/vod: before the Foghorn call, in the intent tx |

All three are keyed by the same identity — `(tenant_id, kind, artifact_hash)` — and the
intent's `request_id` is the ledger key Foghorn matches. `origin_cluster_id` on the intent
selects which Foghorn cluster the sweep and ack drain talk to; it MUST be non-empty (an
empty one can never resolve a Foghorn), enforced both in `upsertCreationIntent` and by a
CHECK constraint.

### Origin cluster is resolved once (DVR)

`startDVR` resolves the origin cluster ONCE (`resolveDVROriginCluster`: stream identity →
storage node cluster → this Foghorn's own cell) and reuses that single value for both the
Commodore intent/business row (via `RegisterDVR`) and Foghorn's own `foghorn.artifacts`
row. Resolving independently on each plane could record different origins and strand an
empty-cluster intent forever; if all fallbacks are empty the create fails closed rather
than persisting an unconvergeable intent.

## Convergence sweep (Commodore)

Convergence and the ack drain run as INDEPENDENT loops (`runCreationIntentSweep` launches
both), so a long convergence pass never delays discharging ack obligations. The convergence
loop claims a batch of pending rows past a grace window under a `lease_token` + `leased_until`
(so multiple replicas are safe; every terminal transition is CAS-guarded on the token) and
processes the batch with bounded concurrency (`creationIntentSweepWorkers`) so it finishes
within the lease. Each intent's Foghorn `GetArtifactCreationStatus` runs under its own RPC
deadline; the resulting terminal/attempt DB write settles under a fresh, detached context so a
timed-out RPC still records its outcome. Outcomes map as:

| Foghorn outcome   | Action                                                                                               |
| ----------------- | ---------------------------------------------------------------------------------------------------- |
| COMMITTED         | finish the catalog row (clip insert / dvr-vod no-op), terminalize `committed`                        |
| REJECTED          | remove any catalog-only business row, terminalize `aborted`                                          |
| ACCEPTED          | in-flight — leave pending at ANY age (a stranded accept is Foghorn's expiry worker's job)            |
| MISSING           | leave pending until `creationIntentMissingDeadline`, then bounded-abort (create RPC lost in transit) |
| IDENTITY_MISMATCH | invariant violation — leave pending, surface the error, NEVER abort                                  |

An ambiguous RPC error (Foghorn unreachable) never counts as a rejection.

## Durable ack obligation (Commodore → Foghorn)

A terminal transition resolved from a KNOWN Foghorn command (COMMITTED/REJECTED) sets
`command_ack_pending` in the SAME transaction (seeding `command_ack_attempts=0`,
`command_ack_next_at=NOW()`). Foghorn stamps `consumed_at` on ack and GCs the command after
a retention horizon; the durable obligation guarantees Foghorn is told before it may reclaim
the row. A MISSING-_abort_ (no command ever existed) sets no obligation.

### Lease vs retry schedule (two separate axes)

`drainCreationCommandAcks` claims a batch of DUE obligations — `command_ack_pending` AND
retry-due (`command_ack_next_at` past/NULL) AND NOT currently leased
(`command_ack_leased_until` past/NULL) — in one `FOR UPDATE SKIP LOCKED` statement that stamps
the lease (`command_ack_leased_until = NOW() + creationIntentAckLease`) AND a fresh per-pass
`command_ack_lease_token`. The claim touches neither the attempt count nor the retry schedule.

The batch is processed CONCURRENTLY (bounded worker pool, `creationIntentAckWorkers`) so it
finishes inside the lease. The lease budget covers the WHOLE window, since the lease clock starts
at the claim's `NOW()`: the claim query itself (`creationIntentClaimTimeout`), a scheduling margin
(`creationIntentLeaseMargin`), and the per-item worst case over `ceil(batch/workers)` waves — one
Foghorn RPC plus the settlement write(s) each item can incur (the convergence sweep can settle
twice: a failed terminal write followed by its attempt note). `TestAckLeaseCoversWorstCaseBatch`
and `TestSweepLeaseCoversWorstCaseBatch` pin the arithmetic.

Every post-claim mutation is CAS-fenced on the lease token (`command_ack_lease_token` for the ack
drain, `lease_token` for the convergence sweep): a stale worker whose lease expired and was
reclaimed by another replica matches ZERO rows, so it can never double-increment attempts, clear
the new owner's lease, nor otherwise mutate a reclaimed row. Fencing makes a lease overrun
non-corrupting — it only risks a duplicated (idempotent) RPC — while the budget above makes an
overrun unlikely. A crashed worker's lease simply expires.

Per-row ack outcome (`ackAndClearCommand`):

| Outcome                                            | Effect                                                                                                                                                                                                            |
| -------------------------------------------------- | ----------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| COMMITTED / REJECTED                               | discharge: clear `command_ack_pending`, set `command_acked_at`                                                                                                                                                    |
| MISSING                                            | idempotent discharge + anomaly log — the command is set only on an already-terminal intent, so a MISSING command means Foghorn consumed+GC'd it past retention; nothing left to converge (a prior clear was lost) |
| ACCEPTED / IDENTITY_MISMATCH / unknown / RPC error | non-discharge: back off (`command_ack_attempts++`, push `command_ack_next_at` by a capped exponential — base 30s, ceiling 15m), release the lease                                                                 |

IDENTITY_MISMATCH is always fail-closed: it backs off but NEVER discharges. The backoff (itself
CAS-fenced on the token) is the ONLY path that touches the retry schedule; the claim never does.
Clear, backoff, and ack are all idempotent, so redundant work across replicas is harmless. Each
settlement write runs under a fresh DB context DETACHED from the Foghorn RPC deadline, so a
DeadlineExceeded RPC still durably records its outcome instead of silently keeping its queue slot.

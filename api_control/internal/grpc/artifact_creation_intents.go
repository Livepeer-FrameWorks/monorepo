package grpc

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"frameworks/api_control/internal/database/commodoredb"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	sharedpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/shared"

	"github.com/google/uuid"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// Artifact creation-intent kinds and terminal states. An intent is written
// BEFORE the cross-service Foghorn create so a crash or a lost/ambiguous response
// leaves a recoverable record; the sweep drains 'pending' intents to a durable
// terminal outcome — a live catalog row ('committed') or a clean absence
// ('aborted', catalog-only row removed) — resolved from Foghorn's command ledger.
const (
	creationIntentKindClip = "clip"
	creationIntentKindDVR  = "dvr"
	creationIntentKindVOD  = "vod"
)

// Sweep cadence. grace is how long a pending intent must be untouched before the
// sweep queries Foghorn: it must exceed the in-band create window (notably the
// DVR path, where RegisterDVR writes the intent BEFORE Foghorn inserts its own
// artifact row), so a still-in-flight create is never misread as rejected.
// leaseTTL bounds how long one sweeper owns a claimed batch before another may
// reclaim it, so a crashed sweeper's claims do not strand. The lease clock starts when the
// claim UPDATE stamps leased_until = NOW(), so leaseTTL must cover the WHOLE budget: the claim
// query itself (up to creationIntentClaimTimeout, since NOW() is fixed at statement start and
// the scan runs before processing begins), PLUS the per-item worst case — the status RPC and
// EVERY post-claim settlement write (a failed terminal transition can be followed by an attempt
// note: two settlements) — for ceil(batch/workers) waves, PLUS a scheduling margin. Ownership is
// nonetheless fenced on lease_token at EVERY post-claim mutation, so even a lease overrun cannot
// let a stale worker mutate a reclaimed row (it only risks a duplicated RPC).
// TestSweepLeaseCoversWorstCaseBatch pins the arithmetic.
const (
	creationIntentSweepInterval = 30 * time.Second
	creationIntentSweepGrace    = 3 * time.Minute
	creationIntentSweepBatch    = 50
	creationIntentSweepWorkers  = 16
	creationIntentLeaseTTL      = 4 * time.Minute
	// creationIntentMissingDeadline bounds the truly-MISSING case: a pending intent
	// Foghorn reports outcome=MISSING for — the create RPC was lost in transit, or the
	// nested RegisterDVR round-trip's response stranded the intent with no Foghorn command.
	// Foghorn distinguishes MISSING from an in-flight ACCEPTED explicitly, so the sweep
	// bounded-aborts only MISSING (never ACCEPTED, which it leaves pending at any age).
	// The deadline (measured from the intent's created_at) is deliberately far longer than
	// Foghorn's accepted-strand expiry (15m) plus any create-RPC window, so a Foghorn-HELD
	// 'accepted' create that later strands is terminalized 'rejected' by Foghorn's expiry
	// worker and resolves via the REJECTED branch well before a healthy intent could reach
	// this bound; only a persistently-MISSING command is bounded-aborted, so it never polls
	// forever.
	creationIntentMissingDeadline = 60 * time.Minute
)

// Ack-drain lease + concurrency. The claim stamps command_ack_leased_until = NOW() +
// creationIntentAckLease AND a fresh command_ack_lease_token, and the due-query excludes
// still-leased rows, so a claimed batch is off-limits to other replicas until the lease
// passes. The batch is processed CONCURRENTLY (creationIntentAckWorkers at a time): each item
// is one RPC plus exactly one settlement write (clear OR backoff). Like the sweep lease, the
// budget includes the claim query (creationIntentClaimTimeout) and a scheduling margin, since
// the lease clock starts at the claim's NOW() (see TestAckLeaseCoversWorstCaseBatch). Every
// settlement CAS-fences on command_ack_lease_token, so a stale worker whose lease was reclaimed
// mutates zero rows regardless of timing. The lease is distinct from the retry schedule
// (command_ack_next_at): only a non-discharging outcome pushes the backoff.
const (
	creationIntentAckLease   = 3 * time.Minute
	creationIntentAckWorkers = 16
)

// Shared Foghorn-RPC and claim/scheduling budgets. creationIntentRPCTimeout bounds a single
// Foghorn round-trip (resolve + status/ack); creationIntentClaimTimeout bounds the batch-claim
// query (its context) whose NOW() starts the lease clock; creationIntentLeaseMargin is headroom
// for the row scan and goroutine scheduling between claim and processing. All three feed the
// lease-budget arithmetic the tests assert, so a timeout and its lease can never silently drift.
const (
	creationIntentRPCTimeout   = 15 * time.Second
	creationIntentClaimTimeout = 30 * time.Second
	creationIntentLeaseMargin  = 15 * time.Second
)

// creationIntentDBSettleTimeout bounds the DB write that records a sweep outcome — a
// terminal transition, a retry backoff, or an attempt note — AFTER the Foghorn RPC. The
// settle context is fresh and DETACHED from the RPC deadline: a DeadlineExceeded Foghorn
// call leaves its own context cancelled, but the bookkeeping it promises (attempts++,
// command_ack_next_at, lease release, terminal status) MUST still persist, or a poisoned
// row keeps its NULLS-FIRST head-of-queue slot and starves healthy obligations.
const creationIntentDBSettleTimeout = 10 * time.Second

// settleDBContext returns a fresh bounded context for a post-RPC DB settlement write,
// detached from the caller's (possibly already-expired) RPC deadline but retaining its
// values, so the settlement runs even when the RPC that preceded it timed out.
func settleDBContext(parent context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.WithoutCancel(parent), creationIntentDBSettleTimeout)
}

// errIntentCASMiss reports that a guarded terminal transition matched no row —
// another sweeper already terminalized the intent, or the claimed lease was lost.
// The terminalizer rolls back with no destructive effect when this occurs.
var errIntentCASMiss = errors.New("creation intent no longer claimable")

// clipCreationPayload carries the clip fields the convergence sweep needs to
// complete a commodore.clips row for a clip whose create committed in Foghorn but
// whose response Commodore never observed. The fulfilled start_time/duration are
// NOT here — they are authoritative in Foghorn and fetched via the creation-status
// query; everything else is captured at request time.
type clipCreationPayload struct {
	ClipID           string  `json:"clip_id"`
	UserID           string  `json:"user_id"`
	StreamID         string  `json:"stream_id"`
	InternalName     string  `json:"internal_name"`
	PlaybackID       string  `json:"playback_id"`
	Title            string  `json:"title"`
	Description      string  `json:"description"`
	ClipMode         string  `json:"clip_mode"`
	RequestedParams  string  `json:"requested_params"`
	OriginClusterID  string  `json:"origin_cluster_id"`
	RetentionUnixSec *int64  `json:"retention_unix_sec,omitempty"`
	RequiresAuth     bool    `json:"requires_auth"`
	PlaybackPolicy   *string `json:"playback_policy,omitempty"`
	WebhookSecretEnc *string `json:"webhook_secret_enc,omitempty"`
}

// upsertCreationIntent durably records a pending creation intent and returns the
// request_id that is PERSISTED for this (tenant, kind, hash) identity. It is
// idempotent on that identity: a re-drive of the same create does not fork a second
// saga and does not resurrect a terminalized intent (the ON CONFLICT clause only
// self-assigns request_id, never resets status/payload). Crucially it RETURNS the
// stored request_id — the existing one on conflict, the fresh one on insert — so the
// caller keys the Foghorn command ledger on the request_id the intent actually
// carries, not a freshly minted one that would mismatch. payload may be nil for
// kinds whose business row is written before the Foghorn call (vod, dvr).
func upsertCreationIntent(ctx context.Context, q commodoredb.DBTX, tenantID, kind, artifactHash, requestID, originClusterID string, payload any) (string, error) {
	// origin_cluster_id keys Foghorn resolution for both convergence and the ack
	// drain; an empty one can never converge (resolveFoghornForCluster rejects it), so
	// reject it here rather than persist an unconvergeable pending intent.
	if originClusterID == "" {
		return "", fmt.Errorf("creation intent (%s/%s) requires a non-empty origin_cluster_id", kind, artifactHash)
	}
	var payloadJSON []byte
	if payload != nil {
		var err error
		payloadJSON, err = json.Marshal(payload)
		if err != nil {
			return "", err
		}
	}
	payloadArg := sql.NullString{}
	if len(payloadJSON) > 0 {
		payloadArg = sql.NullString{String: string(payloadJSON), Valid: true}
	}
	persistedRequestID, err := commodoredb.New(q).UpsertArtifactCreationIntent(ctx, commodoredb.UpsertArtifactCreationIntentParams{
		TenantID: tenantID, Kind: kind, ArtifactHash: artifactHash, RequestID: requestID,
		OriginClusterID: originClusterID, Payload: payloadArg,
	})
	if err != nil {
		return "", err
	}
	return persistedRequestID, nil
}

// terminalizeCreationIntent atomically transitions a pending intent to a terminal
// status AND applies its business-row mutation in ONE transaction. The transition
// is CAS-guarded on status='pending' (and, when leaseToken is non-empty, on the
// claimed lease_token) so two sweepers never both terminalize the same intent: the
// loser's guarded UPDATE matches 0 rows, the tx rolls back with NO business-row
// effect, and it returns errIntentCASMiss. mutate (nil for kinds whose row already
// exists) runs only after the CAS matched, so an abort's row delete or a clip
// commit's row insert never happens for an intent another worker already
// terminalized.
//
// ackPending sets command_ack_pending in the SAME transaction as the terminal
// transition: a terminal outcome resolved from a KNOWN Foghorn command (a commit, or a
// definitive rejection) owes Foghorn a durable ack, drained later by
// drainCreationCommandAcks. Setting the flag also seeds the drain schedule —
// command_ack_attempts=0 and command_ack_next_at=NOW() (due immediately) — so the first
// drain claims it at once; each claim atomically backs it off, and a terminal-consumed ack
// then clears it. A MISSING-abort (no Foghorn command exists) passes false — there is
// nothing to ack, so the schedule stays NULL.
func (s *CommodoreServer) terminalizeCreationIntent(ctx context.Context, r creationIntentRow, newStatus, reason, leaseToken string, ackPending bool, mutate func(context.Context, *sql.Tx) error) error {
	// Settle under a fresh DB context: the caller's status-RPC deadline may already be
	// expired, but this terminal transition must still commit durably.
	ctx, cancel := settleDBContext(ctx)
	defer cancel()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback() //nolint:errcheck // best-effort rollback of an uncommitted tx

	queries := commodoredb.New(tx)
	var n int64
	if leaseToken != "" {
		n, err = queries.TerminalizeClaimedArtifactCreationIntent(ctx, commodoredb.TerminalizeClaimedArtifactCreationIntentParams{
			NewStatus: newStatus, Reason: reason, AckPending: ackPending, TenantID: r.tenantID,
			Kind: r.kind, ArtifactHash: r.artifactHash, LeaseToken: leaseToken,
		})
	} else {
		n, err = queries.TerminalizeArtifactCreationIntent(ctx, commodoredb.TerminalizeArtifactCreationIntentParams{
			NewStatus: newStatus, Reason: reason, AckPending: ackPending, TenantID: r.tenantID,
			Kind: r.kind, ArtifactHash: r.artifactHash,
		})
	}
	if err != nil {
		return err
	}
	if n == 0 {
		return errIntentCASMiss
	}
	if mutate != nil {
		if err := mutate(ctx, tx); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// commitCreationIntent terminalizes a pending intent to 'committed'. mutate writes
// the live catalog row (clips) or is nil when the business row already exists
// (vod/dvr). A commit always resolves from a KNOWN-COMMITTED Foghorn command, so the
// intent owes a durable ack: command_ack_pending is set in the same transaction.
func (s *CommodoreServer) commitCreationIntent(ctx context.Context, r creationIntentRow, leaseToken string, mutate func(context.Context, *sql.Tx) error) error {
	return s.terminalizeCreationIntent(ctx, r, "committed", "", leaseToken, true, mutate)
}

// abortCreationIntent terminalizes a pending intent to 'aborted' and removes any
// catalog-only business row in the same transaction. It writes NO tombstone marker:
// a definitively-rejected create never had a Foghorn artifact/revision, so a clean
// row absence is the terminal state — tombstones exist only for real deletions
// carrying a real Foghorn revision. ackPending is true when the abort resolves from a
// real Foghorn command (a definitive rejection); false for a MISSING-abort, which has
// no command to ack.
func (s *CommodoreServer) abortCreationIntent(ctx context.Context, r creationIntentRow, leaseToken, reason string, ackPending bool) error {
	return s.terminalizeCreationIntent(ctx, r, "aborted", reason, leaseToken, ackPending, deleteCatalogOnlyBusinessRow(r))
}

// deleteCatalogOnlyBusinessRow removes the business registry row for an aborted
// create. vod/dvr rows are written before the Foghorn call and clip rows only on
// commit, so for a clip abort the delete is a harmless no-op.
func deleteCatalogOnlyBusinessRow(r creationIntentRow) func(context.Context, *sql.Tx) error {
	return func(ctx context.Context, tx *sql.Tx) error {
		queries := commodoredb.New(tx)
		params := commodoredb.DeleteCatalogOnlyVODParams{TenantID: r.tenantID, ArtifactHash: r.artifactHash}
		switch r.kind {
		case creationIntentKindVOD:
			return queries.DeleteCatalogOnlyVOD(ctx, params)
		case creationIntentKindDVR:
			return queries.DeleteCatalogOnlyDVR(ctx, commodoredb.DeleteCatalogOnlyDVRParams(params))
		case creationIntentKindClip:
			return queries.DeleteCatalogOnlyClip(ctx, commodoredb.DeleteCatalogOnlyClipParams(params))
		default:
			return fmt.Errorf("abort: unknown kind %q", r.kind)
		}
	}
}

// noteCreationIntentAttempt records an inconclusive sweep pass (Foghorn
// unreachable / ambiguous) without leaving the pending state, so the intent is
// retried on the next sweep rather than falsely terminalized. CAS-fenced on the claimed
// lease_token: a stale convergence worker whose claim was reclaimed (or whose lease expired
// and was re-claimed by another replica) must not bump attempts on the new owner's row.
func (s *CommodoreServer) noteCreationIntentAttempt(ctx context.Context, r creationIntentRow, reason string) {
	// Settle under a fresh DB context so an attempt note still records even when the note
	// follows a timed-out status RPC (whose context is already cancelled).
	ctx, cancel := settleDBContext(ctx)
	defer cancel()
	if err := commodoredb.New(s.db).NoteArtifactCreationIntentAttempt(ctx, commodoredb.NoteArtifactCreationIntentAttemptParams{
		Reason: reason, TenantID: r.tenantID, Kind: r.kind, ArtifactHash: r.artifactHash, LeaseToken: r.leaseToken,
	}); err != nil {
		s.logger.WithError(err).Warn("Failed to record creation-intent sweep attempt")
	}
}

// creationIntentRow is one pending intent under convergence. requestID keys
// Foghorn's command ledger for the status query; leaseToken is the claim this
// sweeper stamped, CAS-checked on every terminal transition.
type creationIntentRow struct {
	tenantID        string
	kind            string
	artifactHash    string
	requestID       string
	originClusterID string
	payload         []byte
	leaseToken      string
	// pastMissingDeadline is computed at claim time (created_at older than
	// creationIntentMissingDeadline) so the sweep can bounded-abort an intent Foghorn
	// holds no command for instead of polling it forever.
	pastMissingDeadline bool
}

// runCreationIntentSweep drains pending artifact creation intents to a durable
// terminal outcome. Runs for the lifetime of the binary; safe on every Commodore
// replica because every terminal transition is guarded and idempotent.
func (s *CommodoreServer) runCreationIntentSweep(ctx context.Context) {
	// Convergence and ack-drain run as INDEPENDENT loops: a long convergence pass must not
	// delay discharging durable ack obligations (nor the reverse). Both are safe on every
	// replica because every transition is lease/CAS-guarded and idempotent.
	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); s.runCreationIntentLoop(ctx, s.sweepCreationIntentsOnce) }()
	go func() { defer wg.Done(); s.runCreationIntentLoop(ctx, s.drainCreationCommandAcks) }()
	wg.Wait()
}

// runCreationIntentLoop runs fn immediately (so a restart resumes without waiting a full
// interval) then every creationIntentSweepInterval until ctx is done.
func (s *CommodoreServer) runCreationIntentLoop(ctx context.Context, fn func(context.Context)) {
	ticker := time.NewTicker(creationIntentSweepInterval)
	defer ticker.Stop()
	fn(ctx)
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			fn(ctx)
		}
	}
}

// ackPendingRow is one terminalized intent that owes Foghorn a durable ack.
// commandAckAttempts is the count of prior non-discharging attempts; it drives the
// exponential backoff pushed onto command_ack_next_at when this attempt also fails to
// discharge.
type ackPendingRow struct {
	tenantID           string
	kind               string
	artifactHash       string
	requestID          string
	originClusterID    string
	commandAckAttempts int
	// leaseToken is the ownership fence stamped by the claim that returned this row. Every
	// settlement (clear/backoff) CAS-matches it, so a stale worker whose lease was reclaimed
	// (token replaced by the new claimant) mutates zero rows.
	leaseToken string
}

// drainCreationCommandAcks discharges the durable ack obligation recorded on terminal
// intents. It ATOMICALLY claims the DUE obligations in ONE statement: a FOR UPDATE SKIP LOCKED
// CTE selects rows that are both retry-due (command_ack_next_at NULL or past) and NOT currently
// leased (command_ack_leased_until NULL or past), and the enclosing UPDATE stamps a fresh LEASE
// (command_ack_leased_until = NOW() + creationIntentAckLease) before RETURNING them. The claim
// touches NEITHER command_ack_attempts NOR command_ack_next_at — the lease is a separate axis
// from the retry schedule. The Foghorn RPCs run on the RETURNED rows OUTSIDE any held DB lock,
// CONCURRENTLY under a bounded worker pool so the whole batch finishes well inside the lease;
// while a row is leased no other replica can reselect it (duplicate RPCs / double-incremented
// attempts are thereby prevented), and a crashed worker's lease expires naturally. Per-row: a
// terminal-consumed outcome (or a MISSING, an idempotent discharge) clears the flag; every
// non-discharging outcome pushes the retry backoff (attempts++). The obligation is durable
// column state, so it survives to the next pass and across restarts. Redundant work across
// replicas is harmless: the ack, the clear, and the backoff are all idempotent per pass.
func (s *CommodoreServer) drainCreationCommandAcks(ctx context.Context) {
	scanCtx, cancel := context.WithTimeout(ctx, creationIntentClaimTimeout)
	defer cancel()

	// One ownership token per claim pass. The claim stamps it alongside the lease; every
	// settlement CAS-matches it, so a row reclaimed by another replica (new token) is
	// off-limits to this pass's stale settlements.
	leaseToken := uuid.New().String()
	rows, err := commodoredb.New(s.db).ClaimArtifactCreationCommandAcks(scanCtx, commodoredb.ClaimArtifactCreationCommandAcksParams{
		LeaseInterval: intervalSeconds(creationIntentAckLease),
		LeaseToken:    leaseToken,
		BatchSize:     int32(creationIntentSweepBatch),
	})
	if err != nil {
		s.logger.WithError(err).Warn("Failed to claim pending creation-command acks")
		return
	}
	batch := make([]ackPendingRow, 0, len(rows))
	for _, row := range rows {
		batch = append(batch, ackPendingRow{
			tenantID: row.TenantID, kind: row.Kind, artifactHash: row.ArtifactHash,
			requestID: row.RequestID, originClusterID: row.OriginClusterID,
			commandAckAttempts: int(row.CommandAckAttempts), leaseToken: leaseToken,
		})
	}

	// Process the leased batch CONCURRENTLY (bounded) so it completes within the lease.
	sem := make(chan struct{}, creationIntentAckWorkers)
	var wg sync.WaitGroup
	for _, r := range batch {
		if ctx.Err() != nil {
			break
		}
		sem <- struct{}{}
		wg.Add(1)
		go func(r ackPendingRow) {
			defer wg.Done()
			defer func() { <-sem }()
			s.drainAckForIntent(ctx, r)
		}(r)
	}
	wg.Wait()
}

// drainAckForIntent resolves Foghorn for one ack-pending intent and acks its command. A
// Foghorn it cannot resolve is a non-discharging failure: it is logged (not silently
// discarded) and the obligation is backed off (attempts++, retry schedule pushed) so it
// retries later without blocking newer due obligations.
func (s *CommodoreServer) drainAckForIntent(ctx context.Context, r ackPendingRow) {
	// rpcCtx bounds the Foghorn resolve + ack RPC ONLY. The settlement writes below take the
	// parent ctx and self-bind a fresh DB deadline, so a timed-out ack still records its backoff.
	rpcCtx, cancel := context.WithTimeout(ctx, creationIntentRPCTimeout)
	defer cancel()

	foghornClient, err := s.resolveFoghornForCluster(rpcCtx, r.originClusterID, r.tenantID)
	if err != nil || foghornClient == nil {
		s.logger.WithError(err).WithFields(logging.Fields{
			"tenant_id":     r.tenantID,
			"kind":          r.kind,
			"artifact_hash": r.artifactHash,
		}).Warn("Failed to resolve Foghorn for creation-command ack; backing off obligation")
		s.backoffAckObligation(ctx, r)
		return
	}
	statusClient := sharedpb.NewArtifactCreationStatusServiceClient(foghornClient.Conn())
	s.ackAndClearCommand(ctx, rpcCtx, statusClient, r)
}

// ackAndClearCommand tells Foghorn the terminal command is consumed and branches on the
// returned OUTCOME. Two outcomes DISCHARGE the obligation (clear command_ack_pending,
// command_acked_at set): COMMITTED/REJECTED (Foghorn consumed the terminal command), and
// MISSING — because command_ack_pending is only ever set on an ALREADY-terminal local intent
// (the catalog is already correct), a MISSING ack means Foghorn already consumed+GC'd the
// command past its retention horizon; there is nothing left to converge, so it is an
// IDEMPOTENT discharge, plus an anomaly log (a lost clear past retention is operationally
// notable). Every other outcome KEEPS the obligation and backs it off (attempts++, retry
// schedule pushed): ACCEPTED means the command has not terminalized yet; IDENTITY_MISMATCH is
// an invariant violation — fail closed, never discharge; an RPC error is likewise
// non-discharging.
func (s *CommodoreServer) ackAndClearCommand(ctx, rpcCtx context.Context, statusClient sharedpb.ArtifactCreationStatusServiceClient, r ackPendingRow) {
	resp, err := statusClient.AckArtifactCreationCommand(rpcCtx, &sharedpb.AckArtifactCreationCommandRequest{
		TenantId:     r.tenantID,
		Kind:         r.kind,
		ArtifactHash: r.artifactHash,
		RequestId:    r.requestID,
	})
	if err != nil {
		s.logger.WithError(err).WithFields(logging.Fields{
			"tenant_id":     r.tenantID,
			"kind":          r.kind,
			"artifact_hash": r.artifactHash,
		}).Warn("Creation-command ack RPC failed; backing off obligation")
		s.backoffAckObligation(ctx, r)
		return
	}

	switch resp.GetOutcome() {
	case sharedpb.ArtifactCreationOutcome_ARTIFACT_CREATION_OUTCOME_COMMITTED,
		sharedpb.ArtifactCreationOutcome_ARTIFACT_CREATION_OUTCOME_REJECTED:
		s.clearAckObligation(ctx, r)
	case sharedpb.ArtifactCreationOutcome_ARTIFACT_CREATION_OUTCOME_MISSING:
		// command_ack_pending is only ever set on a TERMINAL local intent, so a MISSING command
		// means Foghorn already consumed+GC'd it past the retention horizon: an idempotent
		// discharge, plus an anomaly signal (a clear was lost past retention).
		s.logger.WithFields(logging.Fields{
			"tenant_id":     r.tenantID,
			"kind":          r.kind,
			"artifact_hash": r.artifactHash,
			"request_id":    r.requestID,
		}).Error("Creation-command ack resolved MISSING: Foghorn already consumed+GC'd the terminal command; discharging idempotently (anomaly: a prior clear was lost past retention)")
		s.clearAckObligation(ctx, r)
	case sharedpb.ArtifactCreationOutcome_ARTIFACT_CREATION_OUTCOME_ACCEPTED:
		// Foghorn's command has not terminalized yet: NOT discharged, back off and retry later.
		s.backoffAckObligation(ctx, r)
	case sharedpb.ArtifactCreationOutcome_ARTIFACT_CREATION_OUTCOME_IDENTITY_MISMATCH:
		s.logger.WithFields(logging.Fields{
			"tenant_id":     r.tenantID,
			"kind":          r.kind,
			"artifact_hash": r.artifactHash,
			"request_id":    r.requestID,
		}).Error("Creation-command ack resolved IDENTITY_MISMATCH: request_id bound to a different artifact at Foghorn; failing closed, backing off, never clearing")
		s.backoffAckObligation(ctx, r)
	default:
		// UNSPECIFIED / any unknown outcome: inconclusive, do not discharge.
		s.logger.WithFields(logging.Fields{
			"tenant_id":     r.tenantID,
			"kind":          r.kind,
			"artifact_hash": r.artifactHash,
			"outcome":       resp.GetOutcome().String(),
		}).Warn("Creation-command ack resolved inconclusive outcome; backing off obligation")
		s.backoffAckObligation(ctx, r)
	}
}

// clearAckObligation discharges the durable ack obligation after a terminal-consumed (or
// idempotent MISSING) outcome (command_ack_pending=FALSE, command_acked_at set, lease + token
// cleared). CAS-fenced on command_ack_lease_token: a stale worker whose lease was reclaimed
// matches zero rows and no-ops. Idempotent; a failed clear leaves the flag set and is retried.
func (s *CommodoreServer) clearAckObligation(ctx context.Context, r ackPendingRow) {
	// Settle under a fresh DB context: the ack RPC may have consumed the caller's deadline,
	// but the discharge must persist so the obligation is not redriven forever.
	ctx, cancel := settleDBContext(ctx)
	defer cancel()
	if err := commodoredb.New(s.db).ClearArtifactCreationCommandAck(ctx, commodoredb.ClearArtifactCreationCommandAckParams{
		TenantID: r.tenantID, Kind: r.kind, ArtifactHash: r.artifactHash, LeaseToken: r.leaseToken,
	}); err != nil {
		s.logger.WithError(err).WithFields(logging.Fields{
			"tenant_id":     r.tenantID,
			"kind":          r.kind,
			"artifact_hash": r.artifactHash,
		}).Warn("Failed to clear creation-command ack obligation after terminal ack; retried next sweep")
	}
}

// backoffAckObligation pushes the RETRY schedule for a non-discharging ack outcome: it
// increments command_ack_attempts and pushes command_ack_next_at forward by a capped
// exponential of the PRE-increment attempt count (base 30s, doubling per attempt, ceiling
// 15m — exponent clamped to 20 to avoid interval overflow), and releases the claim lease +
// token (command_ack_leased_until = NULL, command_ack_lease_token = NULL) so the row becomes
// claimable again once next_at passes. CAS-fenced on command_ack_lease_token: a stale worker
// whose lease was reclaimed matches zero rows, so it can never double-increment attempts nor
// release the new owner's lease. This is the ONLY path that touches the retry schedule.
func (s *CommodoreServer) backoffAckObligation(ctx context.Context, r ackPendingRow) {
	// Settle under a fresh DB context: a DeadlineExceeded ack (the exact case this backoff
	// exists for) leaves the caller's context cancelled, but the backoff — attempts++, the
	// pushed retry schedule, and the lease release — MUST persist, else the poisoned row
	// merely waits out its lease and reclaims its NULLS-FIRST slot, starving healthy rows.
	ctx, cancel := settleDBContext(ctx)
	defer cancel()
	if err := commodoredb.New(s.db).BackoffArtifactCreationCommandAck(ctx, commodoredb.BackoffArtifactCreationCommandAckParams{
		TenantID: r.tenantID, Kind: r.kind, ArtifactHash: r.artifactHash, LeaseToken: r.leaseToken,
	}); err != nil {
		s.logger.WithError(err).WithFields(logging.Fields{
			"tenant_id":     r.tenantID,
			"kind":          r.kind,
			"artifact_hash": r.artifactHash,
		}).Warn("Failed to back off creation-command ack obligation; retried next sweep")
	}
}

func (s *CommodoreServer) sweepCreationIntentsOnce(ctx context.Context) {
	scanCtx, cancel := context.WithTimeout(ctx, creationIntentClaimTimeout)
	defer cancel()

	// Claim a batch under a fresh lease so a concurrent Commodore replica's sweep
	// never processes the same intents: SELECT ... FOR UPDATE SKIP LOCKED picks
	// unclaimed (or lease-expired) pending rows past the grace window, and the
	// stamped lease_token is CAS-checked on every terminal transition. updated_at is
	// left unchanged so the grace window is not reset by claiming.
	leaseToken := uuid.New().String()
	rows, err := commodoredb.New(s.db).ClaimArtifactCreationIntents(scanCtx, commodoredb.ClaimArtifactCreationIntentsParams{
		LeaseToken: leaseToken, LeaseInterval: intervalSeconds(creationIntentLeaseTTL),
		GraceInterval: intervalSeconds(creationIntentSweepGrace), BatchSize: int32(creationIntentSweepBatch),
		MissingInterval: intervalSeconds(creationIntentMissingDeadline),
	})
	if err != nil {
		s.logger.WithError(err).Warn("Failed to claim pending creation intents")
		return
	}
	batch := make([]creationIntentRow, 0, len(rows))
	for _, row := range rows {
		batch = append(batch, creationIntentRow{
			tenantID: row.TenantID, kind: row.Kind, artifactHash: row.ArtifactHash,
			requestID: row.RequestID, originClusterID: row.OriginClusterID,
			payload: []byte(row.Payload), leaseToken: leaseToken, pastMissingDeadline: row.PastMissingDeadline,
		})
	}

	// Process the claimed batch CONCURRENTLY (bounded) so it finishes within the claim lease:
	// sequential processing of a full batch — each convergence up to the 15s status RPC —
	// could outlive creationIntentLeaseTTL and let another replica reclaim rows mid-flight.
	sem := make(chan struct{}, creationIntentSweepWorkers)
	var wg sync.WaitGroup
	for _, r := range batch {
		if ctx.Err() != nil {
			break
		}
		sem <- struct{}{}
		wg.Add(1)
		go func(r creationIntentRow) {
			defer wg.Done()
			defer func() { <-sem }()
			s.convergeCreationIntent(ctx, r)
		}(r)
	}
	wg.Wait()
}

func intervalSeconds(d time.Duration) string {
	return fmt.Sprintf("%d seconds", int(d.Seconds()))
}

// creationIntentAction is the terminal action the sweep takes for a resolved outcome.
type creationIntentAction int

const (
	// creationIntentActionPending leaves the intent pending for the next sweep (in-flight
	// or inconclusive).
	creationIntentActionPending creationIntentAction = iota
	creationIntentActionCommit
	creationIntentActionAbortRejected
	creationIntentActionAbortMissing
)

// creationIntentActionForOutcome maps Foghorn's explicit ledger outcome to the sweep's
// terminal action. COMMITTED commits the intent; REJECTED aborts it. ACCEPTED is
// in-flight and is NEVER aborted regardless of age — a stranded 'accepted' is Foghorn's
// expiry worker's job, not the sweep's. MISSING (no command — a create RPC lost in
// transit) is the ONLY outcome eligible for the bounded abort, and only past the missing
// deadline; before that, and for an UNSPECIFIED outcome, the intent stays pending.
func creationIntentActionForOutcome(outcome sharedpb.ArtifactCreationOutcome, pastMissingDeadline bool) creationIntentAction {
	switch outcome {
	case sharedpb.ArtifactCreationOutcome_ARTIFACT_CREATION_OUTCOME_COMMITTED:
		return creationIntentActionCommit
	case sharedpb.ArtifactCreationOutcome_ARTIFACT_CREATION_OUTCOME_REJECTED:
		return creationIntentActionAbortRejected
	case sharedpb.ArtifactCreationOutcome_ARTIFACT_CREATION_OUTCOME_MISSING:
		if pastMissingDeadline {
			return creationIntentActionAbortMissing
		}
		return creationIntentActionPending
	default:
		// ACCEPTED (in-flight, never aborted) and UNSPECIFIED (inconclusive) both wait.
		return creationIntentActionPending
	}
}

// convergeCreationIntent resolves one pending intent against Foghorn's explicit command
// ledger outcome for the intent's request_id. COMMITTED → finish the catalog entry;
// REJECTED → remove any catalog-only row and abort; ACCEPTED → leave pending (in-flight,
// never aborted at any age); MISSING → leave pending until the intent passes
// creationIntentMissingDeadline, then bounded-abort the truly-missing command;
// IDENTITY_MISMATCH → leave pending and surface the error (an invariant violation, NEVER
// aborted — the request_id is bound to a different artifact at Foghorn). An ambiguous RPC
// error (Foghorn unreachable) leaves the intent pending — never a rejection. A terminal
// transition from a COMMITTED/REJECTED command sets command_ack_pending in the same
// transaction; the ack is drained durably by drainCreationCommandAcks.
func (s *CommodoreServer) convergeCreationIntent(ctx context.Context, r creationIntentRow) {
	// rpcCtx bounds the Foghorn resolve + status RPC ONLY. The terminal/attempt writes below
	// take the parent ctx and self-bind a fresh DB deadline, so a timed-out status query still
	// records its terminal transition or attempt note rather than silently keeping its slot.
	rpcCtx, cancel := context.WithTimeout(ctx, creationIntentRPCTimeout)
	defer cancel()

	foghornClient, err := s.resolveFoghornForCluster(rpcCtx, r.originClusterID, r.tenantID)
	if err != nil || foghornClient == nil {
		s.noteCreationIntentAttempt(ctx, r, "foghorn unresolved for convergence")
		return
	}

	statusClient := sharedpb.NewArtifactCreationStatusServiceClient(foghornClient.Conn())
	resp, err := statusClient.GetArtifactCreationStatus(rpcCtx, &sharedpb.GetArtifactCreationStatusRequest{
		TenantId:     r.tenantID,
		Kind:         r.kind,
		ArtifactHash: r.artifactHash,
		RequestId:    r.requestID,
	})
	if err != nil {
		// Ambiguous: transport error / Foghorn down. Not a rejection.
		s.noteCreationIntentAttempt(ctx, r, "creation-status query failed: "+status.Convert(err).Message())
		return
	}

	if resp.GetOutcome() == sharedpb.ArtifactCreationOutcome_ARTIFACT_CREATION_OUTCOME_IDENTITY_MISMATCH {
		// Foghorn holds a command for this request_id under a DIFFERENT kind/hash. This is
		// an invariant violation, never a missing command — do NOT abort. Leave the intent
		// pending and surface the error so it is visible rather than silently resolved.
		s.logger.WithFields(logging.Fields{
			"tenant_id":     r.tenantID,
			"kind":          r.kind,
			"artifact_hash": r.artifactHash,
			"request_id":    r.requestID,
		}).Error("Creation-status identity mismatch: request_id bound to a different artifact at Foghorn; leaving intent pending (never aborted)")
		s.noteCreationIntentAttempt(ctx, r, "creation-status identity mismatch (never aborted)")
		return
	}

	switch creationIntentActionForOutcome(resp.GetOutcome(), r.pastMissingDeadline) {
	case creationIntentActionCommit:
		s.convergeCommittedIntent(ctx, r, resp)
	case creationIntentActionAbortRejected:
		s.convergeRejectedIntent(ctx, r)
	case creationIntentActionAbortMissing:
		s.convergeMissingIntent(ctx, r)
	default:
		s.noteCreationIntentAttempt(ctx, r, "creation-status in-flight or inconclusive")
	}
}

// convergeMissingIntent bounded-aborts a pending intent Foghorn holds NO command for,
// past creationIntentMissingDeadline (see its doc for why a Foghorn-held accept never
// reaches here). It is the terminal outcome for a create RPC lost in transit or a nested
// RegisterDVR response that stranded the intent: the abort removes any catalog-only
// business row (vod/dvr) and terminalizes the intent to 'aborted', atomically and
// CAS-guarded on the claimed lease — a clean absence, the same terminal shape as a
// definitive rejection. The user re-drives the create idempotently (the Foghorn create
// RPCs dedup by artifact hash). A lost CAS (another sweeper won) is a silent no-op.
func (s *CommodoreServer) convergeMissingIntent(ctx context.Context, r creationIntentRow) {
	// No Foghorn command exists for a MISSING outcome, so there is nothing to ack:
	// command_ack_pending stays false.
	if err := s.abortCreationIntent(ctx, r, r.leaseToken, "foghorn holds no creation command past missing deadline; bounded abort", false); err != nil {
		if errors.Is(err, errIntentCASMiss) {
			return
		}
		s.noteCreationIntentAttempt(ctx, r, "bounded-abort: "+err.Error())
		return
	}
	s.logger.WithFields(logging.Fields{
		"tenant_id":     r.tenantID,
		"kind":          r.kind,
		"artifact_hash": r.artifactHash,
	}).Warn("Creation intent bounded-aborted (foghorn held no command past missing deadline)")
}

// convergeCommittedIntent completes the live catalog outcome through the shared
// terminalizer (CAS-guarded on the claimed lease). For clips the business row is
// written now from the captured payload plus Foghorn's fulfilled timing, under the
// per-artifact advisory lock + deletion-marker check; for vod/dvr the row already
// exists, so only the intent transition runs. A lost CAS (another sweeper won) is a
// silent no-op. The commit sets command_ack_pending in the same transaction; the ack is
// drained durably by drainCreationCommandAcks.
func (s *CommodoreServer) convergeCommittedIntent(ctx context.Context, r creationIntentRow, resp *sharedpb.GetArtifactCreationStatusResponse) {
	var mutate func(context.Context, *sql.Tx) error
	if r.kind == creationIntentKindClip {
		if len(r.payload) == 0 {
			s.noteCreationIntentAttempt(ctx, r, "clip intent missing payload")
			return
		}
		var p clipCreationPayload
		if err := json.Unmarshal(r.payload, &p); err != nil {
			s.noteCreationIntentAttempt(ctx, r, "clip intent payload unparseable")
			return
		}
		startMs, durationMs := resp.GetEffectiveStartMs(), resp.GetEffectiveDurationMs()
		if startMs <= 0 || durationMs <= 0 {
			// Foghorn committed but its fulfilled range is not yet readable; retry
			// rather than persist fabricated timing.
			s.noteCreationIntentAttempt(ctx, r, "committed clip has no fulfilled timing yet")
			return
		}
		mutate = clipCatalogRowMutator(r, p, startMs, durationMs)
	}
	if err := s.commitCreationIntent(ctx, r, r.leaseToken, mutate); err != nil {
		if errors.Is(err, errIntentCASMiss) {
			// Another sweeper (or the inline create) already terminalized this intent.
			return
		}
		if errors.Is(err, errParentStreamDeleted) {
			// COMPENSATION: the parent stream is being deleted, so this committed clip can never be catalogued —
			// and because its catalog row was never written, the stream-deletion cascade (which enumerates
			// commodore.clips) can never find it. So convergence itself must reclaim the orphaned Foghorn artifact:
			// durably DeleteClip, THEN abort the intent. If the compensation delete fails, keep the intent pending
			// (recoverable retry) — never abort while the artifact is still live, and never leave it pending forever
			// once compensated.
			if cErr := s.compensateOrphanedClipIntent(ctx, r); cErr != nil {
				s.noteCreationIntentAttempt(ctx, r, "clip parent deleted; compensation pending: "+cErr.Error())
				return
			}
			if aErr := s.abortCreationIntent(ctx, r, r.leaseToken, "parent stream deleted; committed clip compensated", false); aErr != nil && !errors.Is(aErr, errIntentCASMiss) {
				s.noteCreationIntentAttempt(ctx, r, "clip compensation abort failed: "+aErr.Error())
			}
			return
		}
		s.noteCreationIntentAttempt(ctx, r, "clip catalog completion failed: "+err.Error())
		return
	}
	// The intent terminalized from a COMMITTED command with command_ack_pending set in the
	// same transaction; drainCreationCommandAcks durably acks Foghorn so its GC may later
	// reclaim the row.
	s.logger.WithFields(logging.Fields{
		"tenant_id":     r.tenantID,
		"kind":          r.kind,
		"artifact_hash": r.artifactHash,
	}).Info("Creation intent converged to committed (live catalog)")
}

// compensateOrphanedClipIntent durably deletes the committed Foghorn clip artifact for an intent whose parent stream
// was deleted mid-convergence, so the orphan (never catalogued, so invisible to the stream-deletion cascade) is
// reclaimed. Uses the same idempotent DeleteClip the normal delete path uses, routed to the clip's origin-cluster
// Foghorn; an already-deleted artifact is a benign no-op (childDeleteAcked). Returns an error to keep the intent
// pending for retry if the artifact is still live and the delete could not be confirmed.
func (s *CommodoreServer) compensateOrphanedClipIntent(ctx context.Context, r creationIntentRow) error {
	fc, err := s.resolveFoghornForArtifact(ctx, r.tenantID, r.originClusterID)
	if err != nil {
		return fmt.Errorf("resolve foghorn: %w", err)
	}
	resp, _, dErr := fc.DeleteClip(ctx, r.artifactHash, &r.tenantID)
	return childDeleteAcked("clip", r.artifactHash, resp, dErr)
}

// clipCatalogRowMutator inserts the commodore.clips row a lost-success clip never
// got. It takes the same per-artifact advisory lock the delete/mint paths use and
// checks the deletion marker first: a present marker means the clip was deleted, so
// the insert is skipped (a create cannot resurrect a live row behind a marker) and
// the intent still terminalizes. Idempotent on clip_hash.
func clipCatalogRowMutator(r creationIntentRow, p clipCreationPayload, startMs, durationMs int64) func(context.Context, *sql.Tx) error {
	return func(ctx context.Context, tx *sql.Tx) error {
		queries := commodoredb.New(tx)
		if err := queries.LockArtifactCreationIdentity(ctx, r.tenantID+":clip:"+r.artifactHash); err != nil {
			return err
		}
		_, tErr := queries.GetArtifactDeletionMarkerForUpdate(ctx, commodoredb.GetArtifactDeletionMarkerForUpdateParams{
			TenantID: r.tenantID, Kind: creationIntentKindClip, ArtifactHash: r.artifactHash,
		})
		switch {
		case tErr == nil:
			// Deletion marker present: do not insert a live row behind it.
			return nil
		case errors.Is(tErr, sql.ErrNoRows):
			// No marker → proceed with the insert.
		default:
			return tErr
		}

		retentionArg := sql.NullTime{}
		if p.RetentionUnixSec != nil {
			retentionArg = sql.NullTime{Time: time.Unix(*p.RetentionUnixSec, 0).UTC(), Valid: true}
		}
		policyArg := sql.NullString{}
		if p.PlaybackPolicy != nil {
			policyArg = sql.NullString{String: *p.PlaybackPolicy, Valid: true}
		}
		secretArg := sql.NullString{}
		if p.WebhookSecretEnc != nil {
			secretArg = sql.NullString{String: *p.WebhookSecretEnc, Valid: true}
		}
		// FENCE the parent against a concurrent deletion IN this tx — a clip must not be catalogued behind a stream
		// that is being torn down (the clip's own artifact would then survive the parent's finalization). On a gone
		// parent this returns errParentStreamDeleted; a lost-success convergence keeps the intent pending/recoverable
		// (never terminalizes it, which would strand the committed Foghorn artifact). NOTE: this synchronous lock
		// serializes Commodore transactions only — it does not span Foghorn's separate artifact commit.
		if fErr := fenceParentStreamLive(ctx, tx, r.tenantID, p.StreamID); fErr != nil {
			return fErr
		}
		return queries.InsertConvergedClip(ctx, commodoredb.InsertConvergedClipParams{
			ClipID: p.ClipID, TenantID: r.tenantID, UserID: p.UserID, StreamID: p.StreamID,
			ClipHash: r.artifactHash, InternalName: p.InternalName, PlaybackID: p.PlaybackID,
			Title: sql.NullString{String: p.Title, Valid: true}, Description: sql.NullString{String: p.Description, Valid: true},
			StartTime: startMs, Duration: durationMs, ClipMode: sql.NullString{String: p.ClipMode, Valid: true},
			RequestedParams: p.RequestedParams, OriginClusterID: sql.NullString{String: r.originClusterID, Valid: true},
			RetentionUntil: retentionArg, RequiresAuth: p.RequiresAuth, PlaybackPolicy: policyArg, WebhookSecret: secretArg,
		})
	}
}

// convergeRejectedIntent removes any catalog-only business row for a definitively
// rejected create and aborts the intent, atomically and CAS-guarded on the claimed
// lease. It writes NO tombstone marker — an aborted create never had a Foghorn
// artifact/revision to protect. A lost CAS (another sweeper won) is a silent no-op. The
// abort resolves from a real REJECTED command, so command_ack_pending is set in the same
// transaction; the ack is drained durably by drainCreationCommandAcks.
func (s *CommodoreServer) convergeRejectedIntent(ctx context.Context, r creationIntentRow) {
	if err := s.abortCreationIntent(ctx, r, r.leaseToken, "foghorn definitively rejected create", true); err != nil {
		if errors.Is(err, errIntentCASMiss) {
			return
		}
		s.noteCreationIntentAttempt(ctx, r, "abort: "+err.Error())
		return
	}
	s.logger.WithFields(logging.Fields{
		"tenant_id":     r.tenantID,
		"kind":          r.kind,
		"artifact_hash": r.artifactHash,
	}).Info("Creation intent converged to aborted (catalog-only row removed)")
}

// creationCreateErrorIsDefinitive classifies a Foghorn create RPC error as a
// DEFINITIVE rejection (the artifact was not and will not be created) vs an
// AMBIGUOUS result (the create may have committed before the response was lost).
// Only application-level rejections Foghorn returns before persisting — invalid
// argument / failed precondition and peers — are definitive; transport errors,
// timeouts, and Internal are ambiguous and must be left for convergence.
func creationCreateErrorIsDefinitive(err error) bool {
	if err == nil {
		return false
	}
	switch status.Code(err) {
	case codes.InvalidArgument, codes.FailedPrecondition, codes.NotFound,
		codes.PermissionDenied, codes.Unimplemented, codes.OutOfRange, codes.AlreadyExists:
		return true
	default:
		// Unavailable, DeadlineExceeded, Canceled, Internal, Unknown,
		// ResourceExhausted, Aborted, DataLoss → ambiguous.
		return false
	}
}

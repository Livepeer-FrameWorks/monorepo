package grpc

import (
	"context"
	"errors"
	"time"

	"frameworks/api_balancing/internal/database/foghorndb"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
)

// errCreationCommandIdentityMismatch is returned when a request_id already carries a
// row for a DIFFERENT (tenant, kind, artifact_hash): the caller reused a Commodore
// creation-intent id for another artifact, which must fail the create rather than
// terminalize (or proceed against) an unrelated intent.
var errCreationCommandIdentityMismatch = errors.New("creation command request_id reused for a different artifact identity")

// errCreationCommandNotAccepted is returned when the commit CAS matches no
// 'accepted' row for this attempt (the deadline expiry already rejected it, or no
// accept was recorded). Inside the artifact-insert tx it forces a rollback, so an
// artifact is never persisted behind a non-'accepted' (rejected) ledger row.
var errCreationCommandNotAccepted = errors.New("creation command not in accepted state at commit")

// creationCommandState is the stored ledger state acceptance observed for an attempt.
// It drives the create handler's decision: proceed with the create, short-circuit to
// the idempotent existing-artifact result, or terminally reject — so a retry of a
// terminal (committed/rejected) request never resumes the create's external work.
type creationCommandState int

const (
	// creationCommandUnknown is the zero value returned alongside an error (never used
	// as a decision).
	creationCommandUnknown creationCommandState = iota
	// creationCommandAccepted: the row is 'accepted' (freshly inserted or already ours),
	// or there is no ledger to track (empty request_id) — the create resumes.
	creationCommandAccepted
	// creationCommandCommitted: the row is already 'committed' — the artifact is durable;
	// short-circuit to the idempotent result without redoing external work.
	creationCommandCommitted
	// creationCommandRejected: the row is 'rejected' — the create is terminally rejected
	// and must not proceed.
	creationCommandRejected
)

// Bounded retry for a durable command-ledger write. A create must not proceed on a
// fire-and-forget ledger write, so both the 'accepted' pre-check and the deferred
// terminal write retry a transient DB error a few times before giving up.
const (
	creationLedgerWriteAttempts = 3
	creationLedgerWriteBackoff  = 50 * time.Millisecond
)

// retryLedgerWrite runs write up to creationLedgerWriteAttempts times, backing off
// between tries and aborting early if ctx is cancelled. A deterministic, permanent
// failure — an identity mismatch, or a commit CAS that matched no 'accepted' row — is
// returned immediately without retrying. Returns the last error.
func retryLedgerWrite(ctx context.Context, write func() error) error {
	var err error
	for attempt := 0; attempt < creationLedgerWriteAttempts; attempt++ {
		if err = write(); err == nil {
			return nil
		}
		if errors.Is(err, errCreationCommandIdentityMismatch) || errors.Is(err, errCreationCommandNotAccepted) {
			return err
		}
		if attempt == creationLedgerWriteAttempts-1 {
			break
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(creationLedgerWriteBackoff):
		}
	}
	return err
}

// recordCreationCommandAccepted marks a create attempt in-flight in the durable
// command ledger and returns the STORED state of the row that now exists. The insert
// is idempotent on request_id (a retry of the same attempt is a no-op via ON CONFLICT
// DO NOTHING); the follow-up read confirms the row carries THIS attempt's
// (tenant, kind, artifact_hash) AND reads its status. A mismatch means the request_id
// was reused for a different artifact and returns errCreationCommandIdentityMismatch so
// the create fails rather than proceeding against an unrelated intent. Otherwise the
// stored status decides: 'accepted' → creationCommandAccepted (the create resumes);
// 'committed' → creationCommandCommitted (a terminal retry — the artifact is durable,
// short-circuit to the idempotent result, do NOT redo external work or re-accept);
// 'rejected' → creationCommandRejected (a terminal retry — do NOT proceed). An empty
// request_id (direct-Foghorn callers carry no Commodore intent) records nothing and
// resolves to creationCommandAccepted so the create proceeds.
func recordCreationCommandAccepted(ctx context.Context, db foghorndb.DBTX, requestID, tenantID, kind, artifactHash string) (creationCommandState, error) {
	if requestID == "" {
		return creationCommandAccepted, nil
	}
	queries := foghorndb.New(db)
	if err := queries.InsertAcceptedArtifactCreationCommand(ctx, foghorndb.InsertAcceptedArtifactCreationCommandParams{
		RequestID: requestID, TenantID: tenantID, Kind: kind, ArtifactHash: artifactHash,
	}); err != nil {
		return creationCommandUnknown, err
	}
	// The row is committed by the insert above (autocommit on the pooled handle), so
	// this read always finds it and reports whether it is ours plus its stored status.
	row, err := queries.ReadArtifactCreationCommandIdentity(ctx, foghorndb.ReadArtifactCreationCommandIdentityParams{
		TenantID: tenantID, Kind: kind, ArtifactHash: artifactHash, RequestID: requestID,
	})
	if err != nil {
		return creationCommandUnknown, err
	}
	if !row.IdentityOk {
		return creationCommandUnknown, errCreationCommandIdentityMismatch
	}
	switch row.Status {
	case "committed":
		return creationCommandCommitted, nil
	case "rejected":
		return creationCommandRejected, nil
	default:
		return creationCommandAccepted, nil
	}
}

// recordCreationCommandCommitted records the terminal committed outcome by a
// compare-and-set on the attempt's own 'accepted' row, reading the artifact's
// catalog_revision from the row just inserted in the SAME transaction so the ledger
// carries the real Foghorn revision. Composed into the artifact-insert tx so a
// committed ledger row and the artifact row are durable together. The CAS matches
// only a row that is still 'accepted' AND whose (tenant, kind, artifact_hash) is
// this attempt's, so a rejected row never flips to committed and a reused request_id
// never terminalizes a different intent. Zero rows affected means the deadline expiry
// already rejected this attempt (or no accept was recorded), so it returns
// errCreationCommandNotAccepted to roll the tx back — an artifact is never persisted
// behind a rejected ledger row. A no-op when request_id is empty.
func recordCreationCommandCommitted(ctx context.Context, db foghorndb.DBTX, requestID, tenantID, kind, artifactHash string) error {
	if requestID == "" {
		return nil
	}
	affected, err := foghorndb.New(db).CommitArtifactCreationCommand(ctx, foghorndb.CommitArtifactCreationCommandParams{
		ArtifactHash: artifactHash, TenantID: tenantID, Kind: kind, RequestID: requestID,
	})
	if err != nil {
		return err
	}
	if affected == 0 {
		return errCreationCommandNotAccepted
	}
	return nil
}

// recordCreationCommandRejected records the terminal rejected outcome for a create
// that failed before persisting its artifact, as a compare-and-set on the attempt's
// own 'accepted' row. The CAS matches only a still-'accepted' row whose
// (tenant, kind, artifact_hash) is this attempt's, so it can neither overwrite a
// 'committed' row (a committed create is never later rejected) nor terminalize a
// different intent that reused the request_id. It reports whether it flipped a row:
// applied is false when the CAS matched nothing (the row is already terminal — a
// racing commit or a prior rejection — or is not ours), which the caller treats as an
// already-converged no-op rather than a failure. A no-op (applied false, nil error)
// when request_id is empty.
func recordCreationCommandRejected(ctx context.Context, db foghorndb.DBTX, requestID, tenantID, kind, artifactHash string) (applied bool, err error) {
	if requestID == "" {
		return false, nil
	}
	affected, err := foghorndb.New(db).RejectArtifactCreationCommand(ctx, foghorndb.RejectArtifactCreationCommandParams{
		RequestID: requestID, TenantID: tenantID, Kind: kind, ArtifactHash: artifactHash,
	})
	if err != nil {
		return false, err
	}
	return affected > 0, nil
}

// creationLedgerProgress tracks how far a create handler advanced through the
// durable command ledger so the deferred finalizer can guarantee a terminal
// outcome. accepted is set once the in-flight row is durably written; committed is
// set once the artifact row and its 'committed' ledger row commit together.
type creationLedgerProgress struct {
	accepted  bool
	committed bool
}

// recordCreationCommandAcceptedDurable writes the in-flight 'accepted' row with a
// bounded retry and returns the stored ledger state (see recordCreationCommandAccepted).
// prog.accepted is marked ONLY when the state is creationCommandAccepted (a live create
// this handler must terminalize); it is left false for a committed/rejected retry (there
// is nothing to reject — the finalizer must not act) and on any error. A caller that
// cannot record 'accepted' must fail the RPC before creating anything, so an identity
// mismatch (a reused request_id) or a transient write failure never lets the create
// proceed and never lets the finalizer terminalize an unrelated intent. An empty
// request_id resolves to creationCommandAccepted (the create proceeds) and writes
// nothing. The identity-mismatch error is returned unwrapped so the handler can map it
// to a distinct gRPC code.
func (s *FoghornGRPCServer) recordCreationCommandAcceptedDurable(ctx context.Context, requestID, tenantID, kind, artifactHash string, prog *creationLedgerProgress) (creationCommandState, error) {
	if requestID == "" {
		return creationCommandAccepted, nil
	}
	var state creationCommandState
	if err := retryLedgerWrite(ctx, func() error {
		var wErr error
		state, wErr = recordCreationCommandAccepted(ctx, s.db, requestID, tenantID, kind, artifactHash)
		return wErr
	}); err != nil {
		return creationCommandUnknown, err
	}
	if state == creationCommandAccepted {
		prog.accepted = true
	}
	return state, nil
}

// ensureCreationCommandCommitted CAS-commits the attempt's 'accepted' row on the pooled
// handle (reading catalog_revision from the existing artifact) so the idempotent
// existing-artifact return path never leaves a live artifact behind a forever-'accepted'
// command — which the convergence sweep would poll as in-flight indefinitely. A CAS that
// matched no 'accepted' row (errCreationCommandNotAccepted) means the row is already
// terminal (the original attempt committed, or a concurrent commit won): the artifact's
// existence is the truth, so it is treated as committed. Only a genuine transient DB
// error propagates, failing the RPC so the client re-drives idempotently rather than
// returning success behind an unconverged ledger. A no-op when request_id is empty.
func (s *FoghornGRPCServer) ensureCreationCommandCommitted(ctx context.Context, requestID, tenantID, kind, artifactHash string, prog *creationLedgerProgress) error {
	if requestID == "" {
		return nil
	}
	err := retryLedgerWrite(ctx, func() error {
		return recordCreationCommandCommitted(ctx, s.db, requestID, tenantID, kind, artifactHash)
	})
	if err != nil && !errors.Is(err, errCreationCommandNotAccepted) {
		return err
	}
	prog.committed = true
	return nil
}

// finalizeCreationCommand guarantees every create exit reaches a durable terminal
// ledger outcome. Run via defer with the handler's named return error. When the
// handler returns an error after 'accepted' was recorded but before 'committed',
// the artifact was not inserted (the handler's own control flow proves it), so it
// CAS-rejects the still-'accepted' row. No-op when request_id is empty (direct-Foghorn
// callers carry no intent), when 'accepted' was never recorded (nothing to
// terminalize; the sweep observes an ambiguous absence), or when 'committed' already
// ran. The reject uses a detached context so a client cancellation cannot strand an
// 'accepted' row. A CAS that matches nothing (applied false) is an already-converged
// row — a racing commit won, or the deadline expiry already rejected it — and is not
// an error. A failed write is only logged, because retErr is already non-nil, so the
// create is retried and the idempotent accept pre-check re-drives to a terminal
// outcome, with the deadline-expiry worker as the final backstop.
func (s *FoghornGRPCServer) finalizeCreationCommand(requestID, tenantID, kind, artifactHash string, prog *creationLedgerProgress, retErr error) error {
	if requestID == "" || retErr == nil || !prog.accepted || prog.committed {
		return retErr
	}
	writeCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := retryLedgerWrite(writeCtx, func() error {
		_, rejErr := recordCreationCommandRejected(writeCtx, s.db, requestID, tenantID, kind, artifactHash)
		return rejErr
	}); err != nil {
		s.logger.WithError(err).WithFields(logging.Fields{
			"request_id":    requestID,
			"artifact_hash": artifactHash,
			"kind":          kind,
		}).Warn("Failed to record rejected creation command on error exit; deadline expiry converges to a terminal outcome")
	}
	return retErr
}

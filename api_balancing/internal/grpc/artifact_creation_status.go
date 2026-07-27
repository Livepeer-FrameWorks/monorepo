package grpc

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strconv"

	sharedpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/shared"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// artifactTypeForKind maps a creation-intent kind to the foghorn.artifacts
// artifact_type discriminator. Empty means an unknown kind (reject).
func artifactTypeForKind(kind string) string {
	switch kind {
	case "clip":
		return "clip"
	case "dvr":
		return "dvr"
	case "vod":
		return "vod"
	default:
		return ""
	}
}

// GetArtifactCreationStatus reports the recorded outcome of a single create
// attempt from Foghorn's durable command ledger, keyed by the Commodore
// creation-intent request_id. It is a pure READ: the query the Commodore convergence
// sweep uses to resolve a lost/ambiguous create. 'committed' means Foghorn durably
// holds the artifact (even if the original response was lost); 'rejected' is a
// definitive rejection; a missing or 'accepted' row is in-flight — NEVER inferred as
// a rejection, so a still-running create is not misread. A stranded 'accepted' (a
// handler that crashed between the accept and its terminal write) is terminalized by
// the CreationCommandExpiryJob background worker, not from this read path, so the
// status query never contends with a concurrent commit.
//
// The row is looked up by (tenant_id, request_id) ONLY, then the stored
// kind/artifact_hash is COMPARED to the request. A truly-absent row is MISSING; a row
// bound to a DIFFERENT kind/hash is IDENTITY_MISMATCH — an invariant violation the
// caller must never read as a missing command (a request_id reused for a different
// artifact must fail closed, not trip the sweep's bounded abort against a live one).
func (s *FoghornGRPCServer) GetArtifactCreationStatus(ctx context.Context, req *sharedpb.GetArtifactCreationStatusRequest) (*sharedpb.GetArtifactCreationStatusResponse, error) {
	if req.GetTenantId() == "" {
		return nil, status.Error(codes.InvalidArgument, "tenant_id is required")
	}
	if req.GetRequestId() == "" {
		return nil, status.Error(codes.InvalidArgument, "request_id is required")
	}
	if artifactTypeForKind(req.GetKind()) == "" {
		return nil, status.Errorf(codes.InvalidArgument, "unknown artifact kind: %q", req.GetKind())
	}

	var (
		ledgerStatus    string
		catalogRevision int64
		storedKind      string
		storedHash      string
	)
	err := s.db.QueryRowContext(ctx, `
		SELECT status, catalog_revision, kind, artifact_hash
		  FROM foghorn.artifact_creation_commands
		 WHERE request_id = $1::uuid AND tenant_id = $2::uuid
	`, req.GetRequestId(), req.GetTenantId()).Scan(&ledgerStatus, &catalogRevision, &storedKind, &storedHash)
	if errors.Is(err, sql.ErrNoRows) {
		// No ledger row for this (tenant, request_id): the create handler has not (yet)
		// recorded an outcome, or the RPC was lost in transit. This is the MISSING
		// outcome, DISTINCT from ACCEPTED, so the sweep can bounded-abort a truly-missing
		// command without ever aborting a valid in-flight one.
		return &sharedpb.GetArtifactCreationStatusResponse{
			Outcome: sharedpb.ArtifactCreationOutcome_ARTIFACT_CREATION_OUTCOME_MISSING,
		}, nil
	}
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to read artifact creation command: %v", err)
	}
	if storedKind != req.GetKind() || storedHash != req.GetArtifactHash() {
		// A row exists for this request_id but under a different artifact identity: the
		// request_id was reused for another create. This is an invariant violation, NOT a
		// missing command — reported distinctly so the caller fails closed (never aborts).
		return &sharedpb.GetArtifactCreationStatusResponse{
			Outcome: sharedpb.ArtifactCreationOutcome_ARTIFACT_CREATION_OUTCOME_IDENTITY_MISMATCH,
		}, nil
	}

	switch ledgerStatus {
	case "committed":
		resp := &sharedpb.GetArtifactCreationStatusResponse{
			Outcome:         sharedpb.ArtifactCreationOutcome_ARTIFACT_CREATION_OUTCOME_COMMITTED,
			CatalogRevision: catalogRevision,
		}
		if req.GetKind() == "clip" {
			startMs, durationMs := s.clipFulfilledTimingMs(ctx, req.GetTenantId(), req.GetArtifactHash())
			resp.EffectiveStartMs = startMs
			resp.EffectiveDurationMs = durationMs
		}
		return resp, nil
	case "rejected":
		return &sharedpb.GetArtifactCreationStatusResponse{
			Outcome: sharedpb.ArtifactCreationOutcome_ARTIFACT_CREATION_OUTCOME_REJECTED,
		}, nil
	default:
		// 'accepted' (or any non-terminal state): in-flight, regardless of age — the
		// ACCEPTED outcome, which the sweep NEVER aborts. A stranded 'accepted' past the
		// hard deadline is terminalized 'rejected' by the CreationCommandExpiryJob worker;
		// this read never writes, so it can never race a concurrent commit into a false
		// rejection.
		return &sharedpb.GetArtifactCreationStatusResponse{
			Outcome: sharedpb.ArtifactCreationOutcome_ARTIFACT_CREATION_OUTCOME_ACCEPTED,
		}, nil
	}
}

// AckArtifactCreationCommand resolves the command for this create attempt and reports
// the ledger OUTCOME, discharging the durable-ack obligation ONLY on a genuine terminal
// consumption. It resolves the row by (tenant_id, request_id), then: a terminal row
// (committed/rejected) of this full identity → stamp consumed_at=NOW() and return
// COMMITTED/REJECTED (the obligation is discharged, the retention GC may later delete the
// row); a still-'accepted' row → return ACCEPTED and stamp NOTHING (the command has not
// terminalized, so the obligation must survive); no row → MISSING; a row bound to a
// DIFFERENT kind/hash → IDENTITY_MISMATCH (an invariant violation the caller fails closed
// on). Only COMMITTED/REJECTED consume the command — the caller keeps the obligation
// (with backoff) on every other outcome, so an ack that lands while the command is still
// accepted can never discard the obligation before the command terminalizes. The terminal
// UPDATE is UNGUARDED on consumed_at, so EVERY terminal ack refreshes consumed_at=NOW():
// a retried ack after a lost RESPONSE pushes the retention window (anchored on consumed_at)
// forward, keeping the row alive until Commodore successfully clears its obligation.
func (s *FoghornGRPCServer) AckArtifactCreationCommand(ctx context.Context, req *sharedpb.AckArtifactCreationCommandRequest) (*sharedpb.AckArtifactCreationCommandResponse, error) {
	if req.GetTenantId() == "" {
		return nil, status.Error(codes.InvalidArgument, "tenant_id is required")
	}
	if req.GetRequestId() == "" {
		return nil, status.Error(codes.InvalidArgument, "request_id is required")
	}
	if artifactTypeForKind(req.GetKind()) == "" {
		return nil, status.Errorf(codes.InvalidArgument, "unknown artifact kind: %q", req.GetKind())
	}

	var (
		ledgerStatus string
		storedKind   string
		storedHash   string
	)
	err := s.db.QueryRowContext(ctx, `
		SELECT status, kind, artifact_hash
		  FROM foghorn.artifact_creation_commands
		 WHERE request_id = $1::uuid AND tenant_id = $2::uuid
	`, req.GetRequestId(), req.GetTenantId()).Scan(&ledgerStatus, &storedKind, &storedHash)
	if errors.Is(err, sql.ErrNoRows) {
		return &sharedpb.AckArtifactCreationCommandResponse{
			Outcome: sharedpb.ArtifactCreationOutcome_ARTIFACT_CREATION_OUTCOME_MISSING,
		}, nil
	}
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to read artifact creation command: %v", err)
	}
	if storedKind != req.GetKind() || storedHash != req.GetArtifactHash() {
		return &sharedpb.AckArtifactCreationCommandResponse{
			Outcome: sharedpb.ArtifactCreationOutcome_ARTIFACT_CREATION_OUTCOME_IDENTITY_MISMATCH,
		}, nil
	}

	var outcome sharedpb.ArtifactCreationOutcome
	switch ledgerStatus {
	case "committed":
		outcome = sharedpb.ArtifactCreationOutcome_ARTIFACT_CREATION_OUTCOME_COMMITTED
	case "rejected":
		outcome = sharedpb.ArtifactCreationOutcome_ARTIFACT_CREATION_OUTCOME_REJECTED
	default:
		// 'accepted' (or any non-terminal state): the command has not terminalized, so it
		// is NOT consumed — return ACCEPTED and stamp nothing, keeping the obligation alive.
		return &sharedpb.AckArtifactCreationCommandResponse{
			Outcome: sharedpb.ArtifactCreationOutcome_ARTIFACT_CREATION_OUTCOME_ACCEPTED,
		}, nil
	}

	// Terminal row of the matching identity: stamp consumed_at so the retention GC may later
	// delete it. UNGUARDED on consumed_at, so a repeat ack REFRESHES consumed_at=NOW() and
	// advances the retention window rather than leaving the row aged from its first ack.
	if _, err := s.db.ExecContext(ctx, `
		UPDATE foghorn.artifact_creation_commands
		   SET consumed_at = NOW()
		 WHERE request_id = $1::uuid AND tenant_id = $2::uuid
		   AND kind = $3 AND artifact_hash = $4
		   AND status IN ('committed', 'rejected')
	`, req.GetRequestId(), req.GetTenantId(), req.GetKind(), req.GetArtifactHash()); err != nil {
		return nil, status.Errorf(codes.Internal, "failed to ack artifact creation command: %v", err)
	}
	return &sharedpb.AckArtifactCreationCommandResponse{Outcome: outcome}, nil
}

// clipFulfilledTimingMs recovers a clip's whole-second-aligned fulfilled range
// (the same start/duration CreateClip returned to Commodore) from the durable
// processing job's source params, so the convergence sweep can complete a
// commodore.clips row for a clip whose original create response was lost. The
// processing-job lookup is bound to the request tenant through the tenant-owned
// foghorn.artifacts row so a cross-tenant hash collision cannot leak another
// tenant's timing into this tenant's catalog. Returns zeros when the params are
// absent/unparseable; the sweep leaves the intent pending rather than writing a row
// with fabricated timing.
func (s *FoghornGRPCServer) clipFulfilledTimingMs(ctx context.Context, tenantID, clipHash string) (startMs, durationMs int64) {
	var paramsJSON []byte
	err := s.db.QueryRowContext(ctx, `
		SELECT j.source_params FROM foghorn.processing_jobs j
		 WHERE j.artifact_hash = $1 AND j.source_params IS NOT NULL
		   AND EXISTS (
		       SELECT 1 FROM foghorn.artifacts a
		        WHERE a.artifact_hash = j.artifact_hash
		          AND a.tenant_id = $2::uuid
		          AND a.artifact_type = 'clip'
		   )
		 ORDER BY j.created_at DESC LIMIT 1
	`, clipHash, tenantID).Scan(&paramsJSON)
	if err != nil || len(paramsJSON) == 0 {
		return 0, 0
	}
	var params map[string]string
	if err := json.Unmarshal(paramsJSON, &params); err != nil {
		return 0, 0
	}
	startUnix, err1 := strconv.ParseInt(params["source_start_unix"], 10, 64)
	stopUnix, err2 := strconv.ParseInt(params["source_stop_unix"], 10, 64)
	if err1 != nil || err2 != nil || stopUnix <= startUnix {
		return 0, 0
	}
	return startUnix * 1000, (stopUnix - startUnix) * 1000
}

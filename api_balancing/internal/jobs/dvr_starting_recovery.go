package jobs

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"frameworks/api_balancing/internal/control"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	ipcpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/ipc"
)

// DVR start-dispatch descriptor states, persisted on foghorn.artifacts.dvr_start_dispatch.
const (
	// DVRDispatchStatePending marks a DVR start that was dispatched to a node and is awaiting the
	// node's first progress ack (which promotes the row 'starting'->'recording').
	DVRDispatchStatePending = "pending"
	// DVRDispatchStateStopPending marks a DVR whose compensating stop failed and must be drained: the
	// recovery worker re-sends the stop until the node accepts it (then the node's DVRStopped finalizes
	// the row) or finalizes the row failed once unrecoverable.
	DVRDispatchStateStopPending = "stop_pending"
)

// DVRStartDispatch is the durable descriptor persisted alongside the DVR 'requested'->'starting'
// transition in StartDVR. It carries the target node plus every field needed to rebuild the exact
// DVRStartRequest, so DVRStartingRecoveryJob can idempotently re-dispatch a start whose node ack was
// lost — without re-resolving live source/storage routing.
type DVRStartDispatch struct {
	State             string `json:"state"`
	NodeID            string `json:"node_id"`
	NodeBaseURL       string `json:"node_base_url"`
	SourceRuntimeName string `json:"source_runtime_name"`
	SourceBaseURL     string `json:"source_base_url"`
	// SourceNodeID is the ingest node that accepted this recording's session. An
	// ingest session is node-bound, so a later StartDVR resolving a DIFFERENT live
	// source node for the same stream is a new session: startDVR records it fresh
	// (never adopting this row) and this row is finalized by its own source node's
	// STREAM_END via control.StopDVRForEndedSource. It is also the key
	// StopDVRForEndedSource matches a STREAM_END against to stop the right session.
	SourceNodeID string `json:"source_node_id"`
	// IngestGeneration is the durable ingest-session id (foghorn.ingest_sessions.id)
	// this recording is bound to. It is a PRECISE session identity (tenant, node,
	// connector PID) — unlike SourceNodeID which is node-only — so a same-node reconnect
	// (new PID → new session) is correctly a new recording. Also mirrored to the
	// artifacts.ingest_generation column (the indexed close-side lookup path). Empty for
	// a manual start or when the PID was unavailable; callers fall back to SourceNodeID.
	IngestGeneration string `json:"ingest_generation,omitempty"`
	SegmentSeconds   int32  `json:"segment_seconds"`
	WindowSeconds    int32  `json:"window_seconds"`
	MaxEntries       int32  `json:"max_entries"`
	StreamID         string `json:"stream_id"`
	InternalName     string `json:"internal_name"`
	DispatchedAt     int64  `json:"dispatched_at"`
}

// DVRStartingRecoveryJob is a SERVICE-OWNED reconciliation for DVR recordings stranded in
// 'requested'/'starting'. StartDVR persists the 'requested' row WITH its dvr_start_dispatch descriptor
// (same insert), then advances 'requested'->'starting' BEFORE commanding the storage node; the node's
// first progress report is the authoritative "recording" ack. A crash (or a lost/failed dispatch)
// anywhere from the insert through that ack leaves the row 'requested'/'starting' forever with no other
// repair path. Because the descriptor is durable from the insert, this job converges those rows
// independently of the client retrying:
//
//   - state 'pending', still 'requested'/'starting' past a threshold and WITHIN the hard grace ->
//     re-run the recording-origin registration and RE-DISPATCH SendDVRStart idempotently (the sidecar
//     treats a repeat start for the same hash+stream as an ack, so this never double-records); once the
//     node acks, the row leaves the pending set and stops matching.
//   - state 'stop_pending' within the hard grace -> a compensating stop failed; re-send SendDVRStop
//     until the node accepts it (its DVRStopped then finalizes the row).
//   - either state, past the hard grace with NO node ack/progress -> STOP re-dispatching; send a
//     best-effort compensating stop and finalize the row failed HONESTLY. The deadline is evaluated
//     transport-independently: a node that accepts every resend but never emits progress is finalized
//     just the same, regardless of whether the last send returned success.
//
// Every action is idempotent and (short of the honest terminal finalize) drives no status change or
// lifecycle event itself (the node's progress/stopped reports do), so it is safe on every replica and
// against a concurrent client retry.
type DVRStartingRecoveryJob struct {
	db         *sql.DB
	logger     logging.Logger
	interval   time.Duration
	staleAfter time.Duration // re-dispatch a pending row whose last update is older than this
	failAfter  time.Duration // finalize failed once the original dispatch is older than this grace
	batchSize  int
	stopCh     chan struct{}
	wg         sync.WaitGroup

	// Control-plane seams, defaulted to the package control.* functions; overridable in tests so the
	// worker's deadline/dispatch logic can be exercised without a live gRPC control plane.
	sendStart      func(nodeID string, req *ipcpb.DVRStartRequest) error
	sendStop       func(nodeID string, req *ipcpb.DVRStopRequest) error
	registerOrigin func(ctx context.Context, artifactHash, nodeID, baseURL string) error
	finalizeDVR    func(ctx context.Context, dvrHash string, opts control.FinalizeOptions) (control.FinalizeResult, error)
}

// DVRStartingRecoveryConfig configures the recovery scan.
type DVRStartingRecoveryConfig struct {
	DB         *sql.DB
	Logger     logging.Logger
	Interval   time.Duration // How often to run (default: 1 minute)
	StaleAfter time.Duration // Act on 'starting' rows older than this (default: 2 minutes)
	FailAfter  time.Duration // Finalize unrecoverable rows once dispatch is older than this (default: 15 minutes)
	BatchSize  int           // Max rows per pass (default: 50)
}

// NewDVRStartingRecoveryJob builds the recovery scan with defaulted thresholds.
func NewDVRStartingRecoveryJob(cfg DVRStartingRecoveryConfig) *DVRStartingRecoveryJob {
	interval := cfg.Interval
	if interval == 0 {
		interval = 1 * time.Minute
	}
	staleAfter := cfg.StaleAfter
	if staleAfter == 0 {
		staleAfter = 2 * time.Minute
	}
	failAfter := cfg.FailAfter
	if failAfter == 0 {
		failAfter = 15 * time.Minute
	}
	if failAfter < staleAfter {
		failAfter = staleAfter
	}
	batchSize := cfg.BatchSize
	if batchSize <= 0 {
		batchSize = 50
	}
	return &DVRStartingRecoveryJob{
		db:             cfg.DB,
		logger:         cfg.Logger,
		interval:       interval,
		staleAfter:     staleAfter,
		failAfter:      failAfter,
		batchSize:      batchSize,
		stopCh:         make(chan struct{}),
		sendStart:      control.SendDVRStart,
		sendStop:       control.SendDVRStop,
		registerOrigin: control.RegisterDVRRecordingOrigin,
		finalizeDVR:    control.FinalizeDVR,
	}
}

// Start begins the background reconciliation loop.
func (j *DVRStartingRecoveryJob) Start() {
	j.wg.Add(1)
	go j.run()
	j.logger.Info("DVR starting-recovery job started")
}

// Stop gracefully stops the job.
func (j *DVRStartingRecoveryJob) Stop() {
	close(j.stopCh)
	j.wg.Wait()
	j.logger.Info("DVR starting-recovery job stopped")
}

func (j *DVRStartingRecoveryJob) run() {
	defer j.wg.Done()
	ticker := time.NewTicker(j.interval)
	defer ticker.Stop()

	j.reconcile()
	for {
		select {
		case <-ticker.C:
			j.reconcile()
		case <-j.stopCh:
			return
		}
	}
}

type startingDVRRow struct {
	artifactHash string
	tenantID     string
	status       string
	dispatch     DVRStartDispatch
}

func (j *DVRStartingRecoveryJob) reconcile() {
	if j.db == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	staleSeconds := int64(j.staleAfter.Seconds())
	if staleSeconds <= 0 {
		staleSeconds = 1
	}

	rows, err := j.db.QueryContext(ctx, `
		SELECT a.artifact_hash,
		       a.tenant_id::text,
		       a.status,
		       a.dvr_start_dispatch::text
		FROM foghorn.artifacts a
		WHERE a.artifact_type = 'dvr'
		  AND a.dvr_start_dispatch IS NOT NULL
		  AND a.updated_at < NOW() - ($1 * INTERVAL '1 second')
		  AND (
		        (a.status IN ('requested', 'starting') AND a.dvr_start_dispatch->>'state' = 'pending')
		        -- A stop obligation is drained regardless of user-visible status (including a terminal
		        -- 'failed' surfaced to the user): the control plane keeps re-sending stop until the node's
		        -- DVRStopped acks and clears the obligation (dvr_start_dispatch retains only {node_id}).
		        OR (a.dvr_start_dispatch->>'state' = 'stop_pending')
		      )
		ORDER BY a.updated_at
		LIMIT $2
	`, staleSeconds, j.batchSize)
	if err != nil {
		j.logger.WithError(err).Warn("DVR starting-recovery: failed to scan stranded starts")
		return
	}
	var batch []startingDVRRow
	for rows.Next() {
		var r startingDVRRow
		var descriptorJSON string
		if scanErr := rows.Scan(&r.artifactHash, &r.tenantID, &r.status, &descriptorJSON); scanErr != nil {
			j.logger.WithError(scanErr).Warn("DVR starting-recovery: row scan failed")
			continue
		}
		if unmErr := json.Unmarshal([]byte(descriptorJSON), &r.dispatch); unmErr != nil {
			j.logger.WithError(unmErr).WithField("dvr_hash", r.artifactHash).Warn("DVR starting-recovery: undecodable dispatch descriptor; skipping")
			continue
		}
		batch = append(batch, r)
	}
	if rowsErr := rows.Err(); rowsErr != nil {
		j.logger.WithError(rowsErr).Warn("DVR starting-recovery: row iteration failed")
	}
	rows.Close() //nolint:sqlclosecheck // fully drained into `batch` above before any per-row work

	for _, r := range batch {
		select {
		case <-j.stopCh:
			return
		default:
		}
		j.reconcileOne(ctx, r)
	}
}

func (j *DVRStartingRecoveryJob) reconcileOne(ctx context.Context, r startingDVRRow) {
	d := r.dispatch
	if d.NodeID == "" {
		// No node to talk to — nothing to re-dispatch or stop; fail it honestly so it can't linger.
		j.failUnrecoverable(ctx, r, "DVR start dispatch descriptor has no target node")
		return
	}

	// A stop obligation is drained FIRST, before any deadline logic: once we owe the node a stop we must
	// RE-SEND stop (never start) and must NOT finalize-and-remove the row. A successful send is NOT an ack
	// — the async DVRStopped is — so the obligation persists across passes until that ack clears it
	// (FinalizeDVR clears the obligation, retaining only {node_id}). The user-visible status may already be
	// 'failed' (surfaced when the pending hard grace lapsed) while this drain continues underneath.
	if d.State == DVRDispatchStateStopPending {
		stopReq := &ipcpb.DVRStopRequest{DvrHash: r.artifactHash, RequestId: r.artifactHash, CommandGeneration: control.DVRStopCommandGeneration}
		if stopErr := j.sendStop(d.NodeID, stopReq); stopErr != nil {
			j.logger.WithError(stopErr).WithField("dvr_hash", r.artifactHash).Warn("DVR starting-recovery: stop re-send failed; will retry")
			return
		}
		j.logger.WithField("dvr_hash", r.artifactHash).Info("DVR starting-recovery: re-sent compensating stop; node DVRStopped will finalize and clear the obligation")
		return
	}

	// Hard deadline for a still-'pending' start, evaluated TRANSPORT-INDEPENDENTLY: the node's first
	// DVRProgress is the only signal that promotes a row out of the pending set, so a node that ACCEPTS
	// every resend but never emits progress would otherwise keep the row 'requested'/'starting' forever.
	// Once past the hard grace with no node ack, STOP re-dispatching start. But a recording may in fact be
	// running, so we do NOT abandon it: PERSIST a stop obligation (durable-before-send), send a best-effort
	// compensating stop, and surface 'failed' to the USER while RETAINING the obligation so the drain above
	// keeps reconciling the stop on later passes until the node's DVRStopped acks it. The finalize does not
	// remove the row from the scan (the stop_pending obligation keeps it in scope).
	if d.DispatchedAt > 0 && time.Since(time.Unix(d.DispatchedAt, 0)) > j.failAfter {
		if setErr := j.persistStopObligation(ctx, r.artifactHash, r.tenantID); setErr != nil {
			j.logger.WithError(setErr).WithField("dvr_hash", r.artifactHash).Warn("DVR starting-recovery: failed to persist stop obligation before hard-grace finalize; will retry")
			return
		}
		stopReq := &ipcpb.DVRStopRequest{DvrHash: r.artifactHash, RequestId: r.artifactHash, CommandGeneration: control.DVRStopCommandGeneration}
		if stopErr := j.sendStop(d.NodeID, stopReq); stopErr != nil {
			j.logger.WithError(stopErr).WithField("dvr_hash", r.artifactHash).Warn("DVR starting-recovery: hard-grace compensating stop send failed; stop_pending persisted for drain")
		}
		j.failWithRetainedStopObligation(ctx, r, fmt.Sprintf("no node progress within hard grace (%s); recording unrecoverable", j.failAfter))
		return
	}

	// Durable supersession fence on the start-REDELIVERY path. The in-memory Helmsman
	// command-generation tombstone is lost across a Helmsman restart, so it cannot be the
	// correctness boundary; the boundary is the durable ingest_sessions.ended_at written by
	// the publisher's PUSH_INPUT_CLOSE. A stranded 'pending' start whose ingest generation
	// has already ended is superseded — re-dispatching it would start (or keep) a recording
	// nothing will stop. Convert it to a stop obligation and drain it (the node treats a stop
	// for an unknown/undisturbed hash idempotently). Bounded to rows that carry a generation;
	// a manual start with no generation falls through to the node-scoped STREAM_END backstop.
	if d.IngestGeneration != "" {
		ended, endErr := j.ingestGenerationEnded(ctx, r.tenantID, d.IngestGeneration)
		if endErr != nil {
			j.logger.WithError(endErr).WithField("dvr_hash", r.artifactHash).Warn("DVR starting-recovery: ingest-generation state check failed; will retry")
			return
		}
		if ended {
			if setErr := j.persistStopObligation(ctx, r.artifactHash, r.tenantID); setErr != nil {
				j.logger.WithError(setErr).WithField("dvr_hash", r.artifactHash).Warn("DVR starting-recovery: failed to persist stop obligation for ended ingest generation; will retry")
				return
			}
			stopReq := &ipcpb.DVRStopRequest{DvrHash: r.artifactHash, RequestId: r.artifactHash, CommandGeneration: control.DVRStopCommandGeneration}
			if stopErr := j.sendStop(d.NodeID, stopReq); stopErr != nil {
				j.logger.WithError(stopErr).WithField("dvr_hash", r.artifactHash).Warn("DVR starting-recovery: stop send for ended ingest generation failed; stop_pending persisted for drain")
			}
			j.logger.WithFields(logging.Fields{
				"dvr_hash":          r.artifactHash,
				"ingest_generation": d.IngestGeneration,
			}).Info("DVR starting-recovery: ingest generation ended; converted stranded start to a stop obligation instead of re-dispatching")
			return
		}
	}

	// state 'pending' within the hard grace: re-run the recording-origin registration (idempotent upsert)
	// and re-dispatch the start. The sidecar converges a repeat start for the same hash+stream into an
	// ack, so no double record; the node's first progress report drives the row out of the pending set.
	if regErr := j.registerOrigin(ctx, r.artifactHash, d.NodeID, d.NodeBaseURL); regErr != nil {
		j.logger.WithError(regErr).WithField("dvr_hash", r.artifactHash).Warn("DVR starting-recovery: recording-origin re-registration failed; will retry")
		return
	}
	startReq := &ipcpb.DVRStartRequest{
		DvrHash:           r.artifactHash,
		InternalName:      d.InternalName,
		SourceRuntimeName: d.SourceRuntimeName,
		SourceBaseUrl:     d.SourceBaseURL,
		RequestId:         r.artifactHash,
		StreamId:          d.StreamID,
		CommandGeneration: control.DVRStartCommandGeneration,
		Config: &ipcpb.DVRConfig{
			Enabled:          true,
			Format:           "ts",
			SegmentDuration:  d.SegmentSeconds,
			DvrWindowSeconds: d.WindowSeconds,
			MaxEntries:       d.MaxEntries,
			RetentionUntil:   0,
		},
	}
	if startErr := j.sendStart(d.NodeID, startReq); startErr != nil {
		j.logger.WithError(startErr).WithFields(logging.Fields{
			"dvr_hash": r.artifactHash,
			"node_id":  d.NodeID,
		}).Warn("DVR starting-recovery: start re-dispatch failed; will retry")
		return
	}
	j.logger.WithFields(logging.Fields{
		"dvr_hash": r.artifactHash,
		"node_id":  d.NodeID,
	}).Info("DVR starting-recovery: re-dispatched start; awaiting node progress ack")
}

// ingestGenerationEnded reports whether the durable ingest session bound to a stranded
// start has already ended (its publisher's PUSH_INPUT_CLOSE landed). Fails OPEN on an
// unknown generation (no row): the durable stop paths (StopDVRForIngestSession, the
// STREAM_END backstop) remain the authority on stopping, so a missing session row must
// not strand a legitimate recording; this check only suppresses a provably-superseded
// re-dispatch.
func (j *DVRStartingRecoveryJob) ingestGenerationEnded(ctx context.Context, tenantID, ingestGeneration string) (bool, error) {
	if j.db == nil {
		return false, sql.ErrConnDone
	}
	var ended bool
	err := j.db.QueryRowContext(ctx, `
		SELECT ended_at IS NOT NULL FROM foghorn.ingest_sessions
		 WHERE id = $1::uuid AND tenant_id = $2::uuid
	`, ingestGeneration, tenantID).Scan(&ended)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return ended, nil
}

// persistStopObligation durably marks dvr_start_dispatch.state='stop_pending' BEFORE any compensating
// stop is sent, so the obligation survives a lost stop-ack and the drain re-sends stop (never start).
// Guarded on a still-live status so it never resurrects an already-terminal (deleted) row.
func (j *DVRStartingRecoveryJob) persistStopObligation(ctx context.Context, artifactHash, tenantID string) error {
	if j.db == nil {
		return sql.ErrConnDone
	}
	_, err := j.db.ExecContext(ctx, `
		UPDATE foghorn.artifacts
		   SET dvr_start_dispatch = jsonb_set(COALESCE(dvr_start_dispatch, '{}'::jsonb), '{state}', '"stop_pending"'::jsonb),
		       updated_at = NOW()
		 WHERE artifact_hash = $1 AND artifact_type = 'dvr' AND tenant_id::text = $2
		   AND status IN ('requested', 'starting', 'recording')
	`, artifactHash, tenantID)
	return err
}

// failUnrecoverable terminalizes a stranded start via FinalizeDVR, which commits the terminal FAILED
// state and its FAILED lifecycle event atomically AND clears the start/stop dispatch descriptor.
// Idempotent: a concurrently-finalized row is a no-op inside FinalizeDVR. Used for rows with nothing to
// stop (no target node).
func (j *DVRStartingRecoveryJob) failUnrecoverable(ctx context.Context, r startingDVRRow, reason string) {
	final, finalErr := j.finalizeDVR(ctx, r.artifactHash, control.FinalizeOptions{
		ReportedStatus: "failed",
		ReportedError:  reason,
		StorageNodeID:  r.dispatch.NodeID,
	})
	if finalErr != nil && final.ArtifactStatus == "" {
		j.logger.WithError(finalErr).WithField("dvr_hash", r.artifactHash).Warn("DVR starting-recovery: failed to finalize unrecoverable start; will retry")
		return
	}
	j.logger.WithFields(logging.Fields{
		"dvr_hash": r.artifactHash,
		"reason":   reason,
	}).Warn("DVR starting-recovery: finalized unrecoverable stranded start as failed")
}

// failWithRetainedStopObligation surfaces 'failed' to the USER via FinalizeDVR but RETAINS the
// dvr_start_dispatch stop obligation (RetainStopObligation), so the row stays in the recovery scan and
// the stop drain keeps reconciling until the node's DVRStopped acks (which clears it). Used at the hard
// grace where a recording may still be running on the node.
func (j *DVRStartingRecoveryJob) failWithRetainedStopObligation(ctx context.Context, r startingDVRRow, reason string) {
	final, finalErr := j.finalizeDVR(ctx, r.artifactHash, control.FinalizeOptions{
		ReportedStatus:       "failed",
		ReportedError:        reason,
		StorageNodeID:        r.dispatch.NodeID,
		RetainStopObligation: true,
	})
	if finalErr != nil && final.ArtifactStatus == "" {
		j.logger.WithError(finalErr).WithField("dvr_hash", r.artifactHash).Warn("DVR starting-recovery: failed to surface 'failed' at hard grace; will retry")
		return
	}
	j.logger.WithFields(logging.Fields{
		"dvr_hash": r.artifactHash,
		"reason":   reason,
	}).Warn("DVR starting-recovery: surfaced 'failed' to user at hard grace; stop obligation retained for drain")
}

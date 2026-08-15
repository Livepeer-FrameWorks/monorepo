package jobs

import (
	"context"
	"sync"
	"time"

	"frameworks/api_balancing/internal/control"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	sharedpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/shared"
	"google.golang.org/protobuf/encoding/protojson"
)

// DVRIntentStarter is the StartDVR seam DVRIntentRecoveryJob drives (FoghornGRPCServer).
type DVRIntentStarter interface {
	StartDVRWithSourceHint(ctx context.Context, req *sharedpb.StartDVRRequest, sourceNodeID string) (*sharedpb.StartDVRResponse, error)
}

// DVRIntentRecoveryJob replays the durable DVR start intent for record:true ingest
// sessions whose recording was never created. handlePushRewrite persists the intent on
// the ingest_sessions row (synchronously, before approving the push) and then launches
// StartDVR in a goroutine; if Foghorn crashes in that window the goroutine's work is
// lost and no artifact exists for DVRStartingRecoveryJob to redispatch. This job is the
// crash backstop: it scans active sessions that carry an intent but have no bound DVR
// artifact and replays StartDVR from the persisted request. Idempotent — StartDVR's
// advisory lock, the one-active-DVR-per-generation unique index, and the
// close-before-start fence make a replay that races the fast-path goroutine (or a
// meanwhile-ended session) a safe no-op.
type DVRIntentRecoveryJob struct {
	logger    logging.Logger
	starter   DVRIntentStarter
	interval  time.Duration
	grace     time.Duration // only replay intents older than this (fast path goes first)
	batchSize int
	stopCh    chan struct{}
	wg        sync.WaitGroup

	// startDVR is the injectable start seam (defaults to starter.StartDVRWithSourceHint).
	startDVR func(ctx context.Context, req *sharedpb.StartDVRRequest, sourceNodeID string) (*sharedpb.StartDVRResponse, error)
	// claimIntents is the injectable claim seam (defaults to control.ClaimUnstartedDVRIntents,
	// which atomically leases + counts). The job holds no DB handle of its own.
	claimIntents func(ctx context.Context, olderThan time.Duration, limit int) ([]control.UnstartedDVRIntent, error)
	// failIntent records an EXPLICIT terminal error (defaults to control.FailDVRIntent) for an
	// undecodable payload or a cap-exhausted intent — never a silent exclusion. Tenant-scoped.
	failIntent func(ctx context.Context, tenantID, sessionID, reason string) error
}

// DVRIntentRecoveryConfig configures the job.
type DVRIntentRecoveryConfig struct {
	Logger    logging.Logger
	Starter   DVRIntentStarter
	Interval  time.Duration // How often to scan (default: 30s)
	Grace     time.Duration // Only replay intents older than this (default: 15s)
	BatchSize int           // Max intents per pass (default: 50)
}

// NewDVRIntentRecoveryJob builds the intent-recovery scan with defaulted thresholds.
func NewDVRIntentRecoveryJob(cfg DVRIntentRecoveryConfig) *DVRIntentRecoveryJob {
	interval := cfg.Interval
	if interval == 0 {
		interval = 30 * time.Second
	}
	grace := cfg.Grace
	if grace == 0 {
		grace = 15 * time.Second
	}
	batchSize := cfg.BatchSize
	if batchSize <= 0 {
		batchSize = 50
	}
	j := &DVRIntentRecoveryJob{
		logger:       cfg.Logger,
		starter:      cfg.Starter,
		interval:     interval,
		grace:        grace,
		batchSize:    batchSize,
		stopCh:       make(chan struct{}),
		claimIntents: control.ClaimUnstartedDVRIntents,
		failIntent:   control.FailDVRIntent,
	}
	if cfg.Starter != nil {
		j.startDVR = cfg.Starter.StartDVRWithSourceHint
	}
	return j
}

// Start begins the background reconciliation loop.
func (j *DVRIntentRecoveryJob) Start() {
	j.wg.Add(1)
	go j.run()
	j.logger.Info("DVR intent-recovery job started")
}

// Stop gracefully stops the job.
func (j *DVRIntentRecoveryJob) Stop() {
	close(j.stopCh)
	j.wg.Wait()
	j.logger.Info("DVR intent-recovery job stopped")
}

func (j *DVRIntentRecoveryJob) run() {
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

func (j *DVRIntentRecoveryJob) reconcile() {
	if j.startDVR == nil || j.claimIntents == nil || j.failIntent == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	// Atomically claim (lease + count) a batch — HA-safe, so a sibling replica does not also
	// process these rows until the lease lapses.
	intents, err := j.claimIntents(ctx, j.grace, j.batchSize)
	if err != nil {
		j.logger.WithError(err).Warn("DVR intent-recovery: failed to claim unstarted intents")
		return
	}
	for _, it := range intents {
		select {
		case <-j.stopCh:
			return
		default:
		}
		j.reconcileOne(ctx, it)
	}
}

func (j *DVRIntentRecoveryJob) reconcileOne(ctx context.Context, it control.UnstartedDVRIntent) {
	req := &sharedpb.StartDVRRequest{}
	if err := protojson.Unmarshal(it.Intent, req); err != nil {
		// PERMANENTLY undecodable → an explicit terminal error (never a silent drop). It can
		// never succeed, so record the reason and stop re-claiming it.
		j.logger.WithError(err).WithFields(logging.Fields{
			"session_id":    it.SessionID,
			"internal_name": it.InternalName,
		}).Error("DVR intent-recovery: undecodable DVR intent; recording terminal error")
		if fErr := j.failIntent(ctx, it.TenantID, it.SessionID, "undecodable DVR intent: "+err.Error()); fErr != nil {
			j.logger.WithError(fErr).WithField("session_id", it.SessionID).Warn("DVR intent-recovery: failed to record terminal error for undecodable intent")
		}
		return
	}
	// Bind the replayed start to THIS session so it records fresh and is finalized by the
	// session's own close (the intent blob was serialized before the generation was known).
	req.IngestGeneration = it.SessionID
	req.TenantId = it.TenantID
	req.InternalName = it.InternalName

	resp, err := j.startDVR(ctx, req, it.NodeID)
	if err != nil {
		// TRANSIENT failure (StartDVR is a live RPC to a storage node / control): the claim already
		// leased the row for backoff and counted this attempt. Keep retrying — do NOT terminalize on
		// an attempt cap. A cap-terminal would set dvr_intent_error, which the claim filters out
		// permanently, so a prolonged-but-recoverable storage/control outage would silently and
		// PERMANENTLY disable a required recording even after the platform recovers. The retry is
		// already bounded on the far side: ClaimUnstartedDVRIntents only selects intents whose
		// session is still ACTIVE (ended_at IS NULL), so once the stream ends the intent naturally
		// stops being claimed. Only a STRUCTURALLY invalid (undecodable) intent — which can never
		// succeed — is terminal (handled above).
		j.logger.WithError(err).WithFields(logging.Fields{
			"session_id":    it.SessionID,
			"internal_name": it.InternalName,
			"attempts":      it.Attempts,
		}).Warn("DVR intent-recovery: replay StartDVR failed; leased for backoff, will retry while the session is active")
		return
	}
	j.logger.WithFields(logging.Fields{
		"session_id":    it.SessionID,
		"internal_name": it.InternalName,
		"dvr_hash":      resp.GetDvrHash(),
		"status":        resp.GetStatus(),
	}).Info("DVR intent-recovery: replayed StartDVR from durable intent")
}

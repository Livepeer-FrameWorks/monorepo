package signingkeyuseoutbox

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"frameworks/api_balancing/internal/database/foghorndb"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	"github.com/google/uuid"
)

type Client interface {
	RecordSigningKeyUse(context.Context, string, string) error
}

// Writer makes a queued local JWT verification durable. AsyncRecorder keeps
// this database write and subsequent Commodore delivery outside media requests.
type Writer struct {
	db *sql.DB
}

func NewWriter(db *sql.DB) *Writer { return &Writer{db: db} }

func (w *Writer) RecordSigningKeyUse(ctx context.Context, tenantID, kid string) error {
	if w == nil || w.db == nil {
		return errors.New("signing-key use outbox is unavailable")
	}
	tenantID = strings.TrimSpace(tenantID)
	kid = strings.TrimSpace(kid)
	if tenantID == "" || kid == "" {
		return errors.New("signing-key use requires tenant_id and kid")
	}
	return foghorndb.New(w.db).EnqueueSigningKeyUse(ctx, foghorndb.EnqueueSigningKeyUseParams{
		TenantID: tenantID,
		Kid:      kid,
	})
}

type observationKey struct {
	tenantID string
	kid      string
}

// AsyncRecorder keeps JWT admission independent of local database latency.
// It bounds memory by unique tenant/key pairs and coalesces repeat viewers for
// the same key while a durable outbox write is pending.
type AsyncRecorder struct {
	writer     *Writer
	logger     logging.Logger
	maxPending int
	wake       chan struct{}

	mu      sync.Mutex
	pending map[observationKey]uint64
	serial  uint64
}

func NewAsyncRecorder(writer *Writer, logger logging.Logger) *AsyncRecorder {
	return &AsyncRecorder{
		writer: writer, logger: logger, maxPending: 4096,
		wake: make(chan struct{}, 1), pending: make(map[observationKey]uint64),
	}
}

func (r *AsyncRecorder) RecordSigningKeyUse(_ context.Context, tenantID, kid string) error {
	if r == nil || r.writer == nil {
		return errors.New("signing-key use recorder is unavailable")
	}
	tenantID = strings.TrimSpace(tenantID)
	kid = strings.TrimSpace(kid)
	if tenantID == "" || kid == "" {
		return errors.New("signing-key use requires tenant_id and kid")
	}
	key := observationKey{tenantID: tenantID, kid: kid}
	r.mu.Lock()
	if _, exists := r.pending[key]; !exists && len(r.pending) >= r.maxPending {
		r.mu.Unlock()
		return errors.New("signing-key use recorder is full")
	}
	r.serial++
	r.pending[key] = r.serial
	r.mu.Unlock()
	select {
	case r.wake <- struct{}{}:
	default:
	}
	return nil
}

// SigningKeyUseIsNonBlocking lets the policy evaluator enqueue inline without
// allocating a goroutine for every successful viewer authentication.
func (*AsyncRecorder) SigningKeyUseIsNonBlocking() {}

func (r *AsyncRecorder) Run(ctx context.Context) {
	if r == nil || r.writer == nil {
		return
	}
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-r.wake:
			r.flush(ctx)
		case <-ticker.C:
			r.flush(ctx)
		}
	}
}

func (r *AsyncRecorder) flush(ctx context.Context) {
	type pendingObservation struct {
		key    observationKey
		serial uint64
	}
	r.mu.Lock()
	batch := make([]pendingObservation, 0, 64)
	for key, serial := range r.pending {
		batch = append(batch, pendingObservation{key: key, serial: serial})
		if len(batch) == cap(batch) {
			break
		}
	}
	r.mu.Unlock()

	for _, observation := range batch {
		writeCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
		err := r.writer.RecordSigningKeyUse(writeCtx, observation.key.tenantID, observation.key.kid)
		cancel()
		if err != nil {
			if r.logger != nil {
				r.logger.WithError(err).WithFields(logging.Fields{
					"tenant_id": observation.key.tenantID, "kid": observation.key.kid,
				}).Warn("Failed to persist signing-key use observation")
			}
			continue
		}
		r.mu.Lock()
		if r.pending[observation.key] == observation.serial {
			delete(r.pending, observation.key)
		}
		r.mu.Unlock()
	}
}

type Worker struct {
	db         *sql.DB
	client     Client
	logger     logging.Logger
	interval   time.Duration
	leaseOwner string
}

func NewWorker(db *sql.DB, client Client, logger logging.Logger) *Worker {
	return &Worker{db: db, client: client, logger: logger, interval: 5 * time.Second, leaseOwner: uuid.NewString()}
}

func (w *Worker) Run(ctx context.Context) {
	if w == nil || w.db == nil || w.client == nil {
		return
	}
	w.drain(ctx)
	ticker := time.NewTicker(w.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			w.drain(ctx)
		}
	}
}

const claimBatchSize = 32

// drain claims batches until one comes back short, so delivery keeps up with the enqueue rate instead of being
// capped at one batch per interval. It continues only while every row of the batch was delivered: a failed or
// superseded row can be due again at once, so any such outcome ends the pass until the next tick.
func (w *Worker) drain(ctx context.Context) {
	for {
		claimed, delivered := w.drainBatch(ctx)
		if claimed < claimBatchSize || delivered < claimed || ctx.Err() != nil {
			return
		}
	}
}

func (w *Worker) drainBatch(ctx context.Context) (claimed, delivered int) {
	queries := foghorndb.New(w.db)
	lease := sql.NullString{String: w.leaseOwner, Valid: true}
	rows, err := queries.ClaimDueSigningKeyUses(ctx, foghorndb.ClaimDueSigningKeyUsesParams{LeaseOwner: lease, BatchSize: claimBatchSize})
	if err != nil {
		w.logger.WithError(err).Warn("Failed to load durable signing-key use observations")
		return 0, 0
	}
	var group sync.WaitGroup
	var delivers atomic.Int64
	for _, row := range rows {
		row := row
		group.Add(1)
		go func() {
			defer group.Done()
			if w.deliver(ctx, lease, row) {
				delivers.Add(1)
			}
		}()
	}
	group.Wait()
	return len(rows), int(delivers.Load())
}

// deliver reports whether the observation was delivered and its row settled.
func (w *Worker) deliver(ctx context.Context, lease sql.NullString, row foghorndb.ClaimDueSigningKeyUsesRow) bool {
	queries := foghorndb.New(w.db)
	callCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	err := w.client.RecordSigningKeyUse(callCtx, row.TenantID, row.Kid)
	cancel()
	if err != nil {
		settled, retryErr := queries.RetrySigningKeyUse(ctx, foghorndb.RetrySigningKeyUseParams{
			ID: row.ID, Revision: row.Revision, LeaseOwner: lease,
		})
		if retryErr != nil {
			w.logger.WithError(retryErr).WithField("kid", row.Kid).Error("Failed to reschedule signing-key use observation")
		} else if settled == 0 {
			if _, releaseErr := queries.ReleaseSigningKeyUseLease(ctx, foghorndb.ReleaseSigningKeyUseLeaseParams{ID: row.ID, LeaseOwner: lease}); releaseErr != nil {
				w.logger.WithError(releaseErr).WithField("kid", row.Kid).Warn("Failed to release superseded signing-key use lease")
			}
		}
		w.logger.WithError(err).WithField("kid", row.Kid).Debug("Signing-key use observation remains pending")
		return false
	}
	settled, err := queries.DeleteDeliveredSigningKeyUse(ctx, foghorndb.DeleteDeliveredSigningKeyUseParams{
		ID: row.ID, Revision: row.Revision, LeaseOwner: lease,
	})
	if err != nil {
		w.logger.WithError(err).WithField("kid", row.Kid).Warn("Failed to settle signing-key use observation")
		return false
	}
	if settled == 0 {
		if _, releaseErr := queries.ReleaseSigningKeyUseLease(ctx, foghorndb.ReleaseSigningKeyUseLeaseParams{ID: row.ID, LeaseOwner: lease}); releaseErr != nil {
			w.logger.WithError(releaseErr).WithField("kid", row.Kid).Warn("Failed to release superseded signing-key use lease")
		}
		return false
	}
	return true
}

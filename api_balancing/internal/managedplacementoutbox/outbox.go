package managedplacementoutbox

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
	commodorepb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/commodore"
	"github.com/google/uuid"
)

// Client is the central projection surface. Neither operation participates in
// the local media decision; failures leave the durable obligation pending.
type Client interface {
	RecordStreamActiveCluster(context.Context, string, string, string) (*commodorepb.RecordStreamActiveClusterResponse, error)
	ClearStreamActiveCluster(context.Context, string, string, string) (*commodorepb.ClearStreamActiveClusterResponse, error)
}

// Writer records the latest desired central projection per stream. Replacing a
// pending row advances its revision, which fences a late delivery from deleting
// a newer local decision.
type Writer struct {
	db *sql.DB
}

func NewWriter(db *sql.DB) *Writer { return &Writer{db: db} }

func (w *Writer) Enqueue(ctx context.Context, streamID, tenantID, clusterID string, active bool) error {
	if w == nil || w.db == nil {
		return errors.New("managed-stream placement outbox is unavailable")
	}
	streamID = strings.TrimSpace(streamID)
	tenantID = strings.TrimSpace(tenantID)
	clusterID = strings.TrimSpace(clusterID)
	if streamID == "" || tenantID == "" || clusterID == "" {
		return errors.New("managed-stream placement requires stream, tenant, and cluster IDs")
	}
	return foghorndb.New(w.db).EnqueueManagedStreamPlacement(ctx, foghorndb.EnqueueManagedStreamPlacementParams{
		StreamID: streamID, TenantID: tenantID, ClusterID: clusterID, DesiredActive: active,
	})
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
	rows, err := queries.ClaimDueManagedStreamPlacements(ctx, foghorndb.ClaimDueManagedStreamPlacementsParams{LeaseOwner: lease, BatchSize: claimBatchSize})
	if err != nil {
		w.logger.WithError(err).Warn("Failed to load durable managed-stream placement obligations")
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

// deliver reports whether the placement was delivered and its row settled.
func (w *Worker) deliver(ctx context.Context, lease sql.NullString, row foghorndb.ClaimDueManagedStreamPlacementsRow) bool {
	queries := foghorndb.New(w.db)
	callCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	var err error
	if row.DesiredActive {
		_, err = w.client.RecordStreamActiveCluster(callCtx, row.StreamID, row.TenantID, row.ClusterID)
	} else {
		_, err = w.client.ClearStreamActiveCluster(callCtx, row.StreamID, row.ClusterID, row.TenantID)
	}
	cancel()
	if err != nil {
		settled, retryErr := queries.RetryManagedStreamPlacement(ctx, foghorndb.RetryManagedStreamPlacementParams{ID: row.ID, Revision: row.Revision, LeaseOwner: lease})
		if retryErr != nil {
			w.logger.WithError(retryErr).WithField("stream_id", row.StreamID).Error("Failed to reschedule managed-stream placement obligation")
		} else if settled == 0 {
			if _, releaseErr := queries.ReleaseManagedStreamPlacementLease(ctx, foghorndb.ReleaseManagedStreamPlacementLeaseParams{ID: row.ID, LeaseOwner: lease}); releaseErr != nil {
				w.logger.WithError(releaseErr).WithField("stream_id", row.StreamID).Warn("Failed to release superseded managed-stream placement lease")
			}
		}
		w.logger.WithError(err).WithField("stream_id", row.StreamID).Warn("Managed-stream placement obligation remains pending")
		return false
	}
	settled, err := queries.DeleteDeliveredManagedStreamPlacement(ctx, foghorndb.DeleteDeliveredManagedStreamPlacementParams{ID: row.ID, Revision: row.Revision, LeaseOwner: lease})
	if err != nil {
		w.logger.WithError(err).WithField("stream_id", row.StreamID).Warn("Failed to settle managed-stream placement obligation")
		return false
	}
	if settled == 0 {
		if _, releaseErr := queries.ReleaseManagedStreamPlacementLease(ctx, foghorndb.ReleaseManagedStreamPlacementLeaseParams{ID: row.ID, LeaseOwner: lease}); releaseErr != nil {
			w.logger.WithError(releaseErr).WithField("stream_id", row.StreamID).Warn("Failed to release superseded managed-stream placement lease")
		}
		return false
	}
	return true
}

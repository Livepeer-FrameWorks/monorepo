package pushstatusoutbox

import (
	"context"
	"database/sql"
	"sync"
	"time"

	"frameworks/api_balancing/internal/database/foghorndb"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	"github.com/google/uuid"
	"github.com/prometheus/client_golang/prometheus"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type Client interface {
	UpdatePushTargetStatus(ctx context.Context, targetID, tenantID, status, reasonCode string, lastError *string) error
}

const (
	ReasonUnspecified         = "unspecified"
	ReasonConnected           = "connected"
	ReasonCompleted           = "completed"
	ReasonDestinationRejected = "destination_rejected"
	ReasonNetworkError        = "network_error"
	ReasonProcessError        = "process_error"
	ReasonCapacityExhausted   = "capacity_exhausted"
	ReasonConfigurationError  = "configuration_error"
	ReasonEdgeUpgradeRequired = "edge_upgrade_required"
	ReasonStopped             = "stopped"
)

// Metrics contains bounded-label status-delivery outcomes.
type Metrics struct {
	Outcomes *prometheus.CounterVec
}

func Enqueue(ctx context.Context, db *sql.DB, targetID, tenantID, status, reasonCode string, lastError *string, eventUnixMillis int64) error {
	if reasonCode == "" {
		reasonCode = ReasonUnspecified
	}
	params := foghorndb.EnqueuePushTargetStatusParams{
		TargetID: targetID, TenantID: tenantID, Status: status, ReasonCode: reasonCode, EventUnixMillis: eventUnixMillis,
	}
	if lastError != nil {
		params.LastError = sql.NullString{String: *lastError, Valid: true}
	}
	return foghorndb.New(db).EnqueuePushTargetStatus(ctx, params)
}

type Worker struct {
	db         *sql.DB
	client     Client
	logger     logging.Logger
	interval   time.Duration
	leaseOwner string
	metrics    *Metrics
}

func NewWorker(db *sql.DB, client Client, logger logging.Logger, metricSet ...*Metrics) *Worker {
	var metrics *Metrics
	if len(metricSet) > 0 {
		metrics = metricSet[0]
	}
	return &Worker{db: db, client: client, logger: logger, interval: 5 * time.Second, leaseOwner: uuid.NewString(), metrics: metrics}
}

func (w *Worker) observe(outcome string) {
	if w != nil && w.metrics != nil && w.metrics.Outcomes != nil {
		w.metrics.Outcomes.WithLabelValues(outcome).Inc()
	}
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

func (w *Worker) drain(ctx context.Context) {
	lease := sql.NullString{String: w.leaseOwner, Valid: true}
	rows, err := foghorndb.New(w.db).ClaimDuePushTargetStatuses(ctx, foghorndb.ClaimDuePushTargetStatusesParams{LeaseOwner: lease, BatchSize: 32})
	if err != nil {
		w.observe("claim_error")
		w.logger.WithError(err).Warn("Failed to load durable push-target status obligations")
		return
	}
	var group sync.WaitGroup
	for _, row := range rows {
		row := row
		group.Add(1)
		go func() {
			defer group.Done()
			w.deliver(ctx, lease, row)
		}()
	}
	group.Wait()
}

func (w *Worker) deliver(ctx context.Context, lease sql.NullString, row foghorndb.ClaimDuePushTargetStatusesRow) {
	var lastError *string
	if row.LastError.Valid {
		value := row.LastError.String
		lastError = &value
	}
	callCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	err := w.client.UpdatePushTargetStatus(callCtx, row.TargetID, row.TenantID, row.Status, row.ReasonCode, lastError)
	cancel()
	queries := foghorndb.New(w.db)
	// A deleted target is a tombstone for its status projection. Retrying can
	// never recreate it and would otherwise retain the outbox row forever.
	terminalNotFound := status.Code(err) == codes.NotFound
	if terminalNotFound {
		err = nil
	}
	if err != nil {
		settled, retryErr := queries.RetryPushTargetStatus(ctx, foghorndb.RetryPushTargetStatusParams{ID: row.ID, Revision: row.Revision, LeaseOwner: lease})
		if retryErr != nil {
			w.observe("retry_error")
			w.logger.WithError(retryErr).WithField("target_id", row.TargetID).Error("Failed to reschedule push-target status obligation")
		} else if settled == 0 {
			w.observe("superseded")
			if _, releaseErr := queries.ReleasePushTargetStatusLease(ctx, foghorndb.ReleasePushTargetStatusLeaseParams{ID: row.ID, LeaseOwner: lease}); releaseErr != nil {
				w.logger.WithError(releaseErr).WithField("target_id", row.TargetID).Warn("Failed to release superseded push-target status lease")
			}
		} else {
			w.observe("retry")
		}
		w.logger.WithError(err).WithField("target_id", row.TargetID).Warn("Push-target status obligation remains pending")
		return
	}
	settled, err := queries.DeleteDeliveredPushTargetStatus(ctx, foghorndb.DeleteDeliveredPushTargetStatusParams{ID: row.ID, Revision: row.Revision, LeaseOwner: lease})
	if err != nil {
		w.observe("settle_error")
		w.logger.WithError(err).WithField("target_id", row.TargetID).Warn("Failed to settle delivered push-target status obligation")
	} else if settled == 0 {
		w.observe("superseded")
		if _, releaseErr := queries.ReleasePushTargetStatusLease(ctx, foghorndb.ReleasePushTargetStatusLeaseParams{ID: row.ID, LeaseOwner: lease}); releaseErr != nil {
			w.logger.WithError(releaseErr).WithField("target_id", row.TargetID).Warn("Failed to release superseded push-target status lease")
		}
	} else if terminalNotFound {
		w.observe("terminal_not_found")
	} else {
		w.observe("delivered")
	}
}

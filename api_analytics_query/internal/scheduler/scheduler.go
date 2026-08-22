package scheduler

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"time"

	"frameworks/api_analytics_query/internal/database/meteringdb"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/config"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	"github.com/google/uuid"

	"frameworks/api_analytics_query/internal/handlers"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/database"
)

// Scheduler handles periodic tasks for billing and usage summarization
type Scheduler struct {
	logger            logging.Logger
	billingSummarizer *handlers.BillingSummarizer
	billingTicker     *time.Ticker
	reservationTicker *time.Ticker
	stopChan          chan struct{}
	queries           meteringdb.Querier
	sourceID          string
	ownerID           string
}

// NewScheduler creates a new scheduler instance
func NewScheduler(yugaDB database.PostgresConn, clickhouse database.ClickHouseConn, logger logging.Logger) *Scheduler {
	billingSummarizer := handlers.NewBillingSummarizer(yugaDB, clickhouse, logger)
	hostname, hostnameErr := os.Hostname()
	if hostnameErr != nil || hostname == "" {
		hostname = "periscope-metering"
	}
	ownerID := config.GetEnv("METERING_WORKER_ID", "")
	if ownerID == "" {
		ownerID = hostname + "-" + uuid.NewString()
	}

	return &Scheduler{
		logger:            logger,
		billingSummarizer: billingSummarizer,
		stopChan:          make(chan struct{}),
		queries:           meteringdb.New(yugaDB),
		sourceID:          config.GetEnv("METERING_SOURCE_ID", "periscope-default"),
		ownerID:           ownerID,
	}
}

func (s *Scheduler) runWithLease(ctx context.Context, partitionKey string, leaseDuration time.Duration, run func(context.Context) error) error {
	fencingToken, err := s.queries.AcquireMeteringLease(ctx, meteringdb.AcquireMeteringLeaseParams{
		SourceID: s.sourceID, PartitionKey: partitionKey, OwnerID: s.ownerID, LeaseSeconds: int64(leaseDuration / time.Second),
	})
	if errors.Is(err, sql.ErrNoRows) {
		s.logger.WithField("source_id", s.sourceID).Debug("Metering lease held by another replica")
		return nil
	}
	if err != nil {
		return err
	}
	s.logger.WithFields(logging.Fields{"source_id": s.sourceID, "partition_key": partitionKey, "fencing_token": fencingToken}).Debug("Acquired metering lease")
	return run(ctx)
}

func (s *Scheduler) runOnce(ctx context.Context) error {
	return s.runWithLease(ctx, "finalized", 12*time.Minute, s.billingSummarizer.ProcessPendingUsage)
}

func (s *Scheduler) runReservations(ctx context.Context) error {
	return s.runWithLease(ctx, "reservations", 90*time.Second, s.billingSummarizer.PublishUsageReservations)
}

// Start begins the scheduled tasks
func (s *Scheduler) Start() {
	s.logger.Info("Starting usage summarization scheduler")

	// Robust cursor-based billing runs frequently to keep drafts updated
	// 5-minute interval for faster metering (especially important for prepaid accounts)
	// It handles any period size automatically via cursors
	interval := 5 * time.Minute

	s.logger.WithFields(logging.Fields{
		"interval": interval,
	}).Info("Scheduler interval configured")

	s.billingTicker = time.NewTicker(interval)
	s.reservationTicker = time.NewTicker(time.Minute)
	go s.runFinalizedTasks()
	go s.runReservationTasks()

	// Run initial summarization immediately (in background)
	go func() {
		time.Sleep(10 * time.Second) // Wait for service to fully start
		s.logger.Info("Running initial usage summarization")
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
		defer cancel()
		if err := s.runOnce(ctx); err != nil {
			s.logger.WithError(err).Error("Failed to run initial usage summarization")
		}
	}()
}

// Stop stops all scheduled tasks
func (s *Scheduler) Stop() {
	s.logger.Info("Stopping usage summarization scheduler")

	if s.billingTicker != nil {
		s.billingTicker.Stop()
	}
	if s.reservationTicker != nil {
		s.reservationTicker.Stop()
	}

	close(s.stopChan)
}

func (s *Scheduler) runFinalizedTasks() {
	for {
		select {
		case <-s.billingTicker.C:
			s.logger.Info("Running scheduled usage summarization")
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
			if err := s.runOnce(ctx); err != nil {
				s.logger.WithError(err).Error("Failed to run usage summarization")
			}
			cancel()
		case <-s.stopChan:
			s.logger.Info("Stopping finalized metering task runner")
			return
		}
	}
}

func (s *Scheduler) runReservationTasks() {
	for {
		select {
		case <-s.reservationTicker.C:
			ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
			if err := s.runReservations(ctx); err != nil {
				s.logger.WithError(err).Error("Failed to publish active usage reservations")
			}
			cancel()
		case <-s.stopChan:
			s.logger.Info("Stopping reservation metering task runner")
			return
		}
	}
}

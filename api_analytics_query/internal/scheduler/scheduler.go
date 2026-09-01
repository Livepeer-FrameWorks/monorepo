package scheduler

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"regexp"
	"strings"
	"time"

	"frameworks/api_analytics_query/internal/database/meteringdb"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/config"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	"github.com/google/uuid"

	"frameworks/api_analytics_query/internal/handlers"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/database"
)

var sourceIdentityPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]*$`)

// NormalizeSourceIdentity validates the durable logical-dataset identity used
// by leases, cursors, and emitted reports. Callers must retain the returned
// values so whitespace cannot fork one configured source into two identities.
func NormalizeSourceIdentity(sourceID, sourceRegion string) (string, string, error) {
	sourceID = strings.TrimSpace(sourceID)
	sourceRegion = strings.TrimSpace(sourceRegion)
	if sourceID == "" || len(sourceID) > 128 || !sourceIdentityPattern.MatchString(sourceID) {
		return "", "", fmt.Errorf("METERING_SOURCE_ID %q must match %s and be at most 128 characters", sourceID, sourceIdentityPattern.String())
	}
	if sourceRegion == "" || len(sourceRegion) > 64 || !sourceIdentityPattern.MatchString(sourceRegion) {
		return "", "", fmt.Errorf("METERING_SOURCE_REGION %q must match %s and be at most 64 characters", sourceRegion, sourceIdentityPattern.String())
	}
	return sourceID, sourceRegion, nil
}

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
	initialDelay      time.Duration
}

// NewScheduler creates a new scheduler instance
func NewScheduler(yugaDB database.PostgresConn, clickhouse database.ClickHouseConn, logger logging.Logger, sourceID, sourceRegion string) *Scheduler {
	billingSummarizer := handlers.NewBillingSummarizer(yugaDB, clickhouse, logger, sourceID, sourceRegion)
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
		sourceID:          sourceID,
		ownerID:           ownerID,
		initialDelay:      10 * time.Second,
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

// ValidateSource establishes the immutable source identity before the worker
// starts serving health checks or competing for leases.
func (s *Scheduler) ValidateSource(ctx context.Context) error {
	return s.billingSummarizer.ValidateSource(ctx)
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
	go s.runFinalizedTasks(s.runOnce)
	go s.runReservationTasks()
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

func (s *Scheduler) runFinalizedTasks(run func(context.Context) error) {
	initialTimer := time.NewTimer(s.initialDelay)
	defer initialTimer.Stop()
	initialRun := initialTimer.C

	for {
		select {
		case <-initialRun:
			initialRun = nil
			s.logger.Info("Running initial usage summarization")
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
			if err := run(ctx); err != nil {
				s.logger.WithError(err).Error("Failed to run initial usage summarization")
			}
			cancel()
		case <-s.billingTicker.C:
			s.logger.Info("Running scheduled usage summarization")
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
			if err := run(ctx); err != nil {
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

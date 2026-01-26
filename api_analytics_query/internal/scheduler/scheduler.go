package scheduler

import (
	"context"
	"time"

	"frameworks/pkg/logging"

	"frameworks/api_analytics_query/internal/handlers"
	"frameworks/pkg/database"
)

// Scheduler handles periodic tasks for billing and usage summarization
type Scheduler struct {
	logger            logging.Logger
	billingSummarizer *handlers.BillingSummarizer
	billingTicker     *time.Ticker
	stopChan          chan bool
}

// NewScheduler creates a new scheduler instance
func NewScheduler(yugaDB database.PostgresConn, clickhouse database.ClickHouseConn, logger logging.Logger) *Scheduler {
	billingSummarizer := handlers.NewBillingSummarizer(yugaDB, clickhouse, logger)

	return &Scheduler{
		logger:            logger,
		billingSummarizer: billingSummarizer,
		stopChan:          make(chan bool),
	}
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
	go s.runBillingTasks()

	// Run initial summarization immediately (in background)
	go func() {
		time.Sleep(10 * time.Second) // Wait for service to fully start
		s.logger.Info("Running initial usage summarization")
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
		defer cancel()
		if err := s.billingSummarizer.ProcessPendingUsage(ctx); err != nil {
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

	close(s.stopChan)
}

// runBillingTasks handles the robust cursor-based usage processing
func (s *Scheduler) runBillingTasks() {
	for {
		select {
		case <-s.billingTicker.C:
			s.logger.Info("Running scheduled usage summarization")
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
			if err := s.billingSummarizer.ProcessPendingUsage(ctx); err != nil {
				s.logger.WithError(err).Error("Failed to run usage summarization")
			}
			cancel()
		case <-s.stopChan:
			s.logger.Info("Stopping billing task runner")
			return
		}
	}
}

// TriggerBillingUpdate manually triggers the process
func (s *Scheduler) TriggerBillingUpdate() error {
	s.logger.Info("Manually triggering billing update")
	return s.billingSummarizer.ProcessPendingUsage(context.Background())
}

// TriggerCustomPeriodSummary triggers usage summarization for a custom time period (Legacy/Debug)
func (s *Scheduler) TriggerCustomPeriodSummary(startTime, endTime time.Time) error {
	s.logger.WithFields(logging.Fields{
		"start_time": startTime,
		"end_time":   endTime,
	}).Info("Manually triggering custom period usage summarization")

	return s.billingSummarizer.SummarizeUsageForPeriod(startTime, endTime)
}

package scheduler

import (
	"time"

	"frameworks/pkg/logging"

	"frameworks/api_analytics_query/internal/handlers"
	"frameworks/pkg/database"
)

// Scheduler handles periodic tasks for billing and usage summarization
type Scheduler struct {
	logger            logging.Logger
	billingSummarizer *handlers.BillingSummarizer
	hourlyTicker      *time.Ticker
	dailyTicker       *time.Ticker
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

	// Get intervals from environment variables
	hourlyInterval := s.getHourlyInterval()
	dailyInterval := s.getDailyInterval()

	s.logger.WithFields(logging.Fields{
		"hourly_interval": hourlyInterval,
		"daily_interval":  dailyInterval,
	}).Info("Scheduler intervals configured")

	// Start hourly ticker
	if hourlyInterval > 0 {
		s.hourlyTicker = time.NewTicker(hourlyInterval)
		go s.runHourlyTasks()
	}

	// Start daily ticker
	if dailyInterval > 0 {
		s.dailyTicker = time.NewTicker(dailyInterval)
		go s.runDailyTasks()
	}

	// Run initial summarization for the previous hour (if enabled)
	if hourlyInterval > 0 {
		go func() {
			time.Sleep(10 * time.Second) // Wait for service to fully start
			s.logger.Info("Running initial hourly usage summarization")
			if err := s.billingSummarizer.RunHourlyUsageSummary(); err != nil {
				s.logger.WithError(err).Error("Failed to run initial hourly usage summarization")
			}
		}()
	}
}

// Stop stops all scheduled tasks
func (s *Scheduler) Stop() {
	s.logger.Info("Stopping usage summarization scheduler")

	if s.hourlyTicker != nil {
		s.hourlyTicker.Stop()
	}

	if s.dailyTicker != nil {
		s.dailyTicker.Stop()
	}

	close(s.stopChan)
}

// runHourlyTasks handles hourly usage summarization
func (s *Scheduler) runHourlyTasks() {
	for {
		select {
		case <-s.hourlyTicker.C:
			s.logger.Info("Running scheduled hourly usage summarization")
			if err := s.billingSummarizer.RunHourlyUsageSummary(); err != nil {
				s.logger.WithError(err).Error("Failed to run hourly usage summarization")
			}
		case <-s.stopChan:
			s.logger.Info("Stopping hourly task runner")
			return
		}
	}
}

// runDailyTasks handles daily usage summarization
func (s *Scheduler) runDailyTasks() {
	for {
		select {
		case <-s.dailyTicker.C:
			s.logger.Info("Running scheduled daily usage summarization")
			if err := s.billingSummarizer.RunDailyUsageSummary(); err != nil {
				s.logger.WithError(err).Error("Failed to run daily usage summarization")
			}
		case <-s.stopChan:
			s.logger.Info("Stopping daily task runner")
			return
		}
	}
}

// getHourlyInterval returns the fixed hourly summarization interval.
func (s *Scheduler) getHourlyInterval() time.Duration {
	return time.Hour
}

// getDailyInterval returns the fixed daily summarization interval.
func (s *Scheduler) getDailyInterval() time.Duration {
	return 24 * time.Hour
}

// TriggerHourlySummary manually triggers hourly usage summarization
func (s *Scheduler) TriggerHourlySummary() error {
	s.logger.Info("Manually triggering hourly usage summarization")
	return s.billingSummarizer.RunHourlyUsageSummary()
}

// TriggerDailySummary manually triggers daily usage summarization
func (s *Scheduler) TriggerDailySummary() error {
	s.logger.Info("Manually triggering daily usage summarization")
	return s.billingSummarizer.RunDailyUsageSummary()
}

// TriggerCustomPeriodSummary triggers usage summarization for a custom time period
func (s *Scheduler) TriggerCustomPeriodSummary(startTime, endTime time.Time) error {
	s.logger.WithFields(logging.Fields{
		"start_time": startTime,
		"end_time":   endTime,
	}).Info("Manually triggering custom period usage summarization")

	return s.billingSummarizer.SummarizeUsageForPeriod(startTime, endTime)
}

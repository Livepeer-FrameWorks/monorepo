package fx

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	"github.com/prometheus/client_golang/prometheus"
)

// SyncInterval is how often the syncer refreshes stored rates. The ECB
// publishes one reference date per TARGET business day around 16:00 CET.
const SyncInterval = time.Hour

// Syncer keeps purser.fx_rates current from the ECB feeds and reports the age
// of the newest stored reference date per currency.
type Syncer struct {
	db       *sql.DB
	logger   logging.Logger
	fetch    Fetcher
	ageGauge *prometheus.GaugeVec
	now      func() time.Time
}

// NewSyncer builds a syncer. ageGauge takes one currency label and may be nil.
func NewSyncer(db *sql.DB, logger logging.Logger, fetch Fetcher, ageGauge *prometheus.GaugeVec) *Syncer {
	return &Syncer{db: db, logger: logger, fetch: fetch, ageGauge: ageGauge, now: time.Now}
}

// Start syncs immediately and then every SyncInterval until ctx is done or
// stop is closed.
func (s *Syncer) Start(ctx context.Context, stop <-chan struct{}) {
	s.tick(ctx)
	ticker := time.NewTicker(SyncInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-stop:
			return
		case <-ticker.C:
			s.tick(ctx)
		}
	}
}

func (s *Syncer) tick(ctx context.Context) {
	if err := s.SyncOnce(ctx); err != nil {
		s.logger.WithError(err).Warn("ECB reference rate sync failed")
	}
	if err := s.RefreshAge(ctx); err != nil {
		s.logger.WithError(err).Warn("ECB reference rate age refresh failed")
	}
}

// SyncOnce stores the daily feed, or the 90-day feed when a currency has no
// stored rate or its newest one is already older than MaxReferenceAgeDays, so
// an outage longer than the daily window leaves no gap.
func (s *Syncer) SyncOnce(ctx context.Context) error {
	latest, err := LatestReferenceDates(ctx, s.db)
	if err != nil {
		return err
	}
	now := s.now()
	feed := FeedDaily
	for _, currency := range Currencies {
		referenceDate, ok := latest[currency]
		if !ok || AgeDays(referenceDate, now) > MaxReferenceAgeDays {
			feed = Feed90Days
			break
		}
	}
	rates, err := FetchRates(ctx, s.fetch, feed)
	if err != nil {
		return err
	}
	if err := Upsert(ctx, s.db, rates, now); err != nil {
		return fmt.Errorf("store ECB rates: %w", err)
	}
	return nil
}

// RefreshAge sets the reference age gauge, in seconds since the start of the
// newest stored reference date, for each currency with a stored rate. A
// currency without one has no series.
func (s *Syncer) RefreshAge(ctx context.Context) error {
	if s.ageGauge == nil {
		return nil
	}
	latest, err := LatestReferenceDates(ctx, s.db)
	if err != nil {
		return err
	}
	now := s.now()
	for _, currency := range Currencies {
		referenceDate, ok := latest[currency]
		if !ok {
			s.ageGauge.DeleteLabelValues(currency)
			continue
		}
		s.ageGauge.WithLabelValues(currency).Set(now.Sub(referenceDate).Seconds())
	}
	return nil
}

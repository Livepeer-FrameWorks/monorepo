package diagnostics

import (
	"context"
	"database/sql"
	"fmt"
	"math"
	"strings"
	"time"
)

// BaselineMetric holds the Welford running statistics for a single metric.
type BaselineMetric struct {
	Avg         float64
	M2          float64
	SampleCount int64
}

// StdDev returns the population standard deviation. Returns 0 when fewer
// than 2 samples have been collected (variance is undefined).
func (b BaselineMetric) StdDev() float64 {
	if b.SampleCount < 2 {
		return 0
	}
	return math.Sqrt(b.M2 / float64(b.SampleCount))
}

// Deviation describes how a current metric reading differs from its baseline.
type Deviation struct {
	Metric    string
	Current   float64
	Baseline  float64
	StdDev    float64
	Sigma     float64
	Direction string // "above" or "below"
}

func (d Deviation) String() string {
	return fmt.Sprintf("%s: %.4g (baseline %.4g ± %.4g, %.1fσ %s)",
		d.Metric, d.Current, d.Baseline, d.StdDev, d.Sigma, d.Direction)
}

// BaselineStore abstracts persistence for baseline metrics.
type BaselineStore interface {
	Get(ctx context.Context, tenantID, streamID string) (map[string]BaselineMetric, error)
	Upsert(ctx context.Context, tenantID, streamID string, metrics map[string]BaselineMetric) error
	CleanupStale(ctx context.Context, tenantID string, maxAge time.Duration) (int64, error)
}

// BaselineEvaluator manages Welford running baselines and computes deviations.
type BaselineEvaluator struct {
	store      BaselineStore
	sigmaLimit float64
	minSamples int64
}

// NewBaselineEvaluator creates an evaluator with the given store.
// sigmaLimit controls how many standard deviations count as a deviation (default 2.0).
// minSamples is the minimum sample count before deviations are reported (default 5).
func NewBaselineEvaluator(store BaselineStore, sigmaLimit float64, minSamples int64) *BaselineEvaluator {
	if sigmaLimit <= 0 {
		sigmaLimit = 2.0
	}
	if minSamples <= 0 {
		minSamples = 5
	}
	return &BaselineEvaluator{
		store:      store,
		sigmaLimit: sigmaLimit,
		minSamples: minSamples,
	}
}

// Update feeds current metrics into the Welford running average and persists.
// Only the heartbeat should call this (the sole writer).
func (e *BaselineEvaluator) Update(ctx context.Context, tenantID, streamID string, metrics map[string]float64) error {
	if e == nil || e.store == nil {
		return nil
	}
	existing, err := e.store.Get(ctx, tenantID, streamID)
	if err != nil {
		return fmt.Errorf("load baselines: %w", err)
	}
	if existing == nil {
		existing = make(map[string]BaselineMetric, len(metrics))
	}

	for name, value := range metrics {
		b := existing[name]
		b.SampleCount++
		delta := value - b.Avg
		b.Avg += delta / float64(b.SampleCount)
		delta2 := value - b.Avg
		b.M2 += delta * delta2
		existing[name] = b
	}

	if err := e.store.Upsert(ctx, tenantID, streamID, existing); err != nil {
		return fmt.Errorf("persist baselines: %w", err)
	}
	return nil
}

// Deviations compares current metrics against stored baselines and returns
// those that exceed sigmaLimit standard deviations. Read-only.
func (e *BaselineEvaluator) Deviations(ctx context.Context, tenantID, streamID string, metrics map[string]float64) ([]Deviation, error) {
	if e == nil || e.store == nil {
		return nil, nil
	}
	baselines, err := e.store.Get(ctx, tenantID, streamID)
	if err != nil {
		return nil, fmt.Errorf("load baselines: %w", err)
	}

	var deviations []Deviation
	for name, current := range metrics {
		b, ok := baselines[name]
		if !ok || b.SampleCount < e.minSamples {
			continue
		}
		stddev := b.StdDev()
		if stddev == 0 {
			continue
		}
		diff := current - b.Avg
		sigma := math.Abs(diff) / stddev
		if sigma < e.sigmaLimit {
			continue
		}
		direction := "above"
		if diff < 0 {
			direction = "below"
		}
		deviations = append(deviations, Deviation{
			Metric:    name,
			Current:   current,
			Baseline:  b.Avg,
			StdDev:    stddev,
			Sigma:     sigma,
			Direction: direction,
		})
	}
	return deviations, nil
}

// Cleanup removes baselines not updated within maxAge.
func (e *BaselineEvaluator) Cleanup(ctx context.Context, tenantID string, maxAge time.Duration) error {
	if e == nil || e.store == nil {
		return nil
	}
	_, err := e.store.CleanupStale(ctx, tenantID, maxAge)
	return err
}

// SQLBaselineStore implements BaselineStore using PostgreSQL.
type SQLBaselineStore struct {
	db *sql.DB
}

// NewSQLBaselineStore creates a store backed by the given database.
func NewSQLBaselineStore(db *sql.DB) *SQLBaselineStore {
	return &SQLBaselineStore{db: db}
}

func (s *SQLBaselineStore) Get(ctx context.Context, tenantID, streamID string) (map[string]BaselineMetric, error) {
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("baseline store unavailable")
	}
	rows, err := s.db.QueryContext(ctx,
		`SELECT metric_name, avg_value, m2, sample_count
		 FROM skipper.skipper_baselines
		 WHERE tenant_id = $1 AND stream_id = $2`,
		tenantID, streamID,
	)
	if err != nil {
		return nil, fmt.Errorf("query baselines: %w", err)
	}
	defer rows.Close()

	metrics := make(map[string]BaselineMetric)
	for rows.Next() {
		var name string
		var b BaselineMetric
		if err := rows.Scan(&name, &b.Avg, &b.M2, &b.SampleCount); err != nil {
			return nil, fmt.Errorf("scan baseline: %w", err)
		}
		metrics[name] = b
	}
	return metrics, rows.Err()
}

func (s *SQLBaselineStore) Upsert(ctx context.Context, tenantID, streamID string, metrics map[string]BaselineMetric) error {
	if s == nil || s.db == nil {
		return fmt.Errorf("baseline store unavailable")
	}
	if len(metrics) == 0 {
		return nil
	}

	// Build a single INSERT ... ON CONFLICT DO UPDATE for all metrics.
	var b strings.Builder
	b.WriteString(`INSERT INTO skipper.skipper_baselines (tenant_id, stream_id, metric_name, avg_value, m2, sample_count, updated_at) VALUES `)
	args := make([]any, 0, len(metrics)*6)
	i := 0
	for name, m := range metrics {
		if i > 0 {
			b.WriteString(", ")
		}
		base := i*6 + 1
		fmt.Fprintf(&b, "($%d, $%d, $%d, $%d, $%d, $%d, NOW())",
			base, base+1, base+2, base+3, base+4, base+5)
		args = append(args, tenantID, streamID, name, m.Avg, m.M2, m.SampleCount)
		i++
	}
	b.WriteString(` ON CONFLICT (tenant_id, stream_id, metric_name) DO UPDATE SET avg_value = EXCLUDED.avg_value, m2 = EXCLUDED.m2, sample_count = EXCLUDED.sample_count, updated_at = NOW()`)

	_, err := s.db.ExecContext(ctx, b.String(), args...)
	if err != nil {
		return fmt.Errorf("upsert baselines: %w", err)
	}
	return nil
}

func (s *SQLBaselineStore) CleanupStale(ctx context.Context, tenantID string, maxAge time.Duration) (int64, error) {
	if s == nil || s.db == nil {
		return 0, fmt.Errorf("baseline store unavailable")
	}
	cutoff := time.Now().Add(-maxAge)
	result, err := s.db.ExecContext(ctx,
		`DELETE FROM skipper.skipper_baselines WHERE tenant_id = $1 AND updated_at < $2`,
		tenantID, cutoff,
	)
	if err != nil {
		return 0, fmt.Errorf("cleanup baselines: %w", err)
	}
	return result.RowsAffected()
}

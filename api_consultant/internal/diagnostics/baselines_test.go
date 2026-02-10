package diagnostics

import (
	"context"
	"database/sql/driver"
	"math"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
)

func TestBaselineMetric_StdDev(t *testing.T) {
	tests := []struct {
		name   string
		metric BaselineMetric
		want   float64
	}{
		{"zero samples", BaselineMetric{SampleCount: 0}, 0},
		{"one sample", BaselineMetric{Avg: 5, M2: 0, SampleCount: 1}, 0},
		{"known variance", BaselineMetric{Avg: 10, M2: 20, SampleCount: 5}, math.Sqrt(4)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.metric.StdDev()
			if math.Abs(got-tt.want) > 1e-9 {
				t.Errorf("StdDev() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestDeviation_String(t *testing.T) {
	d := Deviation{
		Metric:    "avg_fps",
		Current:   18.5,
		Baseline:  28.2,
		StdDev:    2.1,
		Sigma:     4.6,
		Direction: "below",
	}
	s := d.String()
	if s == "" {
		t.Fatal("expected non-empty string")
	}
	for _, want := range []string{"avg_fps", "18.5", "28.2", "4.6σ", "below"} {
		if !contains(s, want) {
			t.Errorf("String() = %q, missing %q", s, want)
		}
	}
}

func contains(s, sub string) bool {
	return len(s) >= len(sub) && (s == sub || len(s) > 0 && containsSubstring(s, sub))
}

func containsSubstring(s, sub string) bool {
	for i := 0; i <= len(s)-len(sub); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}

// --- Welford math tests ---

func TestWelfordKnownSequence(t *testing.T) {
	// Feed values [2, 4, 4, 4, 5, 5, 7, 9] — known mean=5, stddev=2
	values := []float64{2, 4, 4, 4, 5, 5, 7, 9}
	store := newMemStore()
	eval := NewBaselineEvaluator(store, 2.0, 5)
	ctx := context.Background()

	for _, v := range values {
		if err := eval.Update(ctx, "t1", "", map[string]float64{"x": v}); err != nil {
			t.Fatalf("Update: %v", err)
		}
	}

	baselines, err := store.Get(ctx, "t1", "")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	b := baselines["x"]
	if b.SampleCount != 8 {
		t.Fatalf("SampleCount = %d, want 8", b.SampleCount)
	}
	if math.Abs(b.Avg-5.0) > 1e-9 {
		t.Errorf("Avg = %v, want 5.0", b.Avg)
	}
	// Population stddev = 2.0, so M2/N = 4.0, M2 = 32
	wantStdDev := 2.0
	gotStdDev := b.StdDev()
	if math.Abs(gotStdDev-wantStdDev) > 1e-9 {
		t.Errorf("StdDev = %v, want %v", gotStdDev, wantStdDev)
	}
}

func TestFirstCycleNoDeviations(t *testing.T) {
	store := newMemStore()
	eval := NewBaselineEvaluator(store, 2.0, 5)
	ctx := context.Background()

	// First update initializes — should produce no deviations even for extreme values.
	if err := eval.Update(ctx, "t1", "", map[string]float64{"fps": 0}); err != nil {
		t.Fatalf("Update: %v", err)
	}
	devs, err := eval.Deviations(ctx, "t1", "", map[string]float64{"fps": 0})
	if err != nil {
		t.Fatalf("Deviations: %v", err)
	}
	if len(devs) != 0 {
		t.Errorf("expected 0 deviations on first cycle, got %d", len(devs))
	}
}

func TestMinSamplesGuard(t *testing.T) {
	store := newMemStore()
	eval := NewBaselineEvaluator(store, 2.0, 5)
	ctx := context.Background()

	// Feed 4 samples (below minSamples=5)
	for i := 0; i < 4; i++ {
		_ = eval.Update(ctx, "t1", "", map[string]float64{"fps": 30})
	}
	// Extreme current value — but not enough samples
	devs, err := eval.Deviations(ctx, "t1", "", map[string]float64{"fps": 0})
	if err != nil {
		t.Fatalf("Deviations: %v", err)
	}
	if len(devs) != 0 {
		t.Errorf("expected 0 deviations with <5 samples, got %d", len(devs))
	}
}

func TestDeviationDetection(t *testing.T) {
	store := newMemStore()
	eval := NewBaselineEvaluator(store, 2.0, 5)
	ctx := context.Background()

	// Build a baseline: 10 samples of fps=30 with slight variation
	for _, v := range []float64{30, 30, 30, 30, 30, 30, 30, 30, 30, 30} {
		_ = eval.Update(ctx, "t1", "", map[string]float64{"fps": v})
	}

	// Value within 2σ — no deviation
	devs, _ := eval.Deviations(ctx, "t1", "", map[string]float64{"fps": 30})
	if len(devs) != 0 {
		t.Errorf("expected no deviations for normal value, got %d", len(devs))
	}

	// Build baseline with variance
	store2 := newMemStore()
	eval2 := NewBaselineEvaluator(store2, 2.0, 5)
	for _, v := range []float64{28, 30, 32, 29, 31, 30, 28, 32, 29, 31} {
		_ = eval2.Update(ctx, "t1", "", map[string]float64{"fps": v})
	}
	// Value far outside baseline (fps=10, baseline ~30 ± ~1.4)
	devs, _ = eval2.Deviations(ctx, "t1", "", map[string]float64{"fps": 10})
	if len(devs) != 1 {
		t.Fatalf("expected 1 deviation, got %d", len(devs))
	}
	if devs[0].Direction != "below" {
		t.Errorf("expected direction=below, got %q", devs[0].Direction)
	}
	if devs[0].Sigma < 2 {
		t.Errorf("expected sigma >= 2, got %v", devs[0].Sigma)
	}
}

func TestNilEvaluator(t *testing.T) {
	var eval *BaselineEvaluator
	ctx := context.Background()

	if err := eval.Update(ctx, "t1", "", map[string]float64{"x": 1}); err != nil {
		t.Fatalf("nil Update should not error: %v", err)
	}
	devs, err := eval.Deviations(ctx, "t1", "", map[string]float64{"x": 1})
	if err != nil {
		t.Fatalf("nil Deviations should not error: %v", err)
	}
	if len(devs) != 0 {
		t.Errorf("nil Deviations should return empty, got %d", len(devs))
	}
	if err := eval.Cleanup(ctx, "t1", time.Hour); err != nil {
		t.Fatalf("nil Cleanup should not error: %v", err)
	}
}

// --- SQL store tests ---

func TestSQLBaselineStore_Get(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()

	rows := sqlmock.NewRows([]string{"metric_name", "avg_value", "m2", "sample_count"}).
		AddRow("fps", 30.0, 10.0, int64(5)).
		AddRow("bitrate", 5000.0, 200.0, int64(10))
	mock.ExpectQuery("SELECT metric_name").
		WithArgs("t1", "").
		WillReturnRows(rows)

	store := NewSQLBaselineStore(db)
	metrics, err := store.Get(context.Background(), "t1", "")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if len(metrics) != 2 {
		t.Fatalf("expected 2 metrics, got %d", len(metrics))
	}
	if metrics["fps"].Avg != 30.0 {
		t.Errorf("fps avg = %v, want 30", metrics["fps"].Avg)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("unmet expectations: %v", err)
	}
}

func TestSQLBaselineStore_Upsert(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()

	mock.ExpectExec("INSERT INTO skipper.skipper_baselines").
		WillReturnResult(sqlmock.NewResult(0, 1))

	store := NewSQLBaselineStore(db)
	err = store.Upsert(context.Background(), "t1", "", map[string]BaselineMetric{
		"fps": {Avg: 30, M2: 10, SampleCount: 5},
	})
	if err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("unmet expectations: %v", err)
	}
}

func TestSQLBaselineStore_UpsertMultiMetric(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()

	// 3 metrics × 6 columns = 18 args. WithArgs validates the count,
	// catching placeholder indexing bugs (e.g. i*5 instead of i*6).
	anyArgs := make([]driver.Value, 18)
	for i := range anyArgs {
		anyArgs[i] = sqlmock.AnyArg()
	}
	mock.ExpectExec("INSERT INTO skipper.skipper_baselines").
		WithArgs(anyArgs...).
		WillReturnResult(sqlmock.NewResult(0, 3))

	store := NewSQLBaselineStore(db)
	err = store.Upsert(context.Background(), "t1", "", map[string]BaselineMetric{
		"bitrate":       {Avg: 5e6, M2: 100, SampleCount: 10},
		"buffer_health": {Avg: 3.0, M2: 0.5, SampleCount: 10},
		"fps":           {Avg: 30.0, M2: 4.0, SampleCount: 10},
	})
	if err != nil {
		t.Fatalf("Upsert multi-metric: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("unmet expectations: %v", err)
	}
}

func TestSQLBaselineStore_UpsertEmpty(t *testing.T) {
	db, _, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()

	store := NewSQLBaselineStore(db)
	if err := store.Upsert(context.Background(), "t1", "", nil); err != nil {
		t.Fatalf("Upsert(nil): %v", err)
	}
}

func TestSQLBaselineStore_CleanupStale(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()

	mock.ExpectExec("DELETE FROM skipper.skipper_baselines").
		WillReturnResult(sqlmock.NewResult(0, 3))

	store := NewSQLBaselineStore(db)
	n, err := store.CleanupStale(context.Background(), "t1", 7*24*time.Hour)
	if err != nil {
		t.Fatalf("CleanupStale: %v", err)
	}
	if n != 3 {
		t.Errorf("expected 3 deleted, got %d", n)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("unmet expectations: %v", err)
	}
}

// --- In-memory store for unit tests ---

type memBaselineStore struct {
	data map[string]map[string]BaselineMetric // key = "tenant|stream"
}

func newMemStore() *memBaselineStore {
	return &memBaselineStore{data: make(map[string]map[string]BaselineMetric)}
}

func (m *memBaselineStore) key(tenantID, streamID string) string {
	return tenantID + "|" + streamID
}

func (m *memBaselineStore) Get(_ context.Context, tenantID, streamID string) (map[string]BaselineMetric, error) {
	metrics := m.data[m.key(tenantID, streamID)]
	if metrics == nil {
		return nil, nil
	}
	cp := make(map[string]BaselineMetric, len(metrics))
	for k, v := range metrics {
		cp[k] = v
	}
	return cp, nil
}

func (m *memBaselineStore) Upsert(_ context.Context, tenantID, streamID string, metrics map[string]BaselineMetric) error {
	k := m.key(tenantID, streamID)
	if m.data[k] == nil {
		m.data[k] = make(map[string]BaselineMetric)
	}
	for name, v := range metrics {
		m.data[k][name] = v
	}
	return nil
}

func (m *memBaselineStore) CleanupStale(_ context.Context, _ string, _ time.Duration) (int64, error) {
	return 0, nil
}

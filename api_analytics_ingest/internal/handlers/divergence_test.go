package handlers

import (
	"context"
	"reflect"
	"testing"

	"github.com/google/uuid"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
)

// scriptedRows returns a single prior-fact row (or none, when vals is nil) so
// the divergence checkers can be driven against a known prior projection.
type scriptedRows struct {
	vals []any
	done bool
}

func (r *scriptedRows) Next() bool {
	if r.vals == nil || r.done {
		return false
	}
	r.done = true
	return true
}
func (r *scriptedRows) Scan(dest ...any) error {
	for i := range dest {
		reflect.ValueOf(dest[i]).Elem().Set(reflect.ValueOf(r.vals[i]))
	}
	return nil
}
func (r *scriptedRows) Close() error { return nil }
func (r *scriptedRows) Err() error   { return nil }

// divergenceClickhouse returns a scripted prior row from Query and captures the
// projection_divergences inserts via the shared captureBatch.
type divergenceClickhouse struct {
	prior []any
	batch *captureBatch
}

func (c *divergenceClickhouse) PrepareBatch(_ context.Context, _ string) (clickhouseBatch, error) {
	return c.batch, nil
}
func (c *divergenceClickhouse) Query(_ context.Context, _ string, _ ...any) (clickhouseRows, error) {
	return &scriptedRows{vals: c.prior}, nil
}
func (c *divergenceClickhouse) Exec(_ context.Context, _ string, _ ...any) error { return nil }

func newDivergenceHandler(prior []any) (*AnalyticsHandler, *captureBatch) {
	batch := &captureBatch{}
	h := &AnalyticsHandler{
		clickhouse: &divergenceClickhouse{prior: prior, batch: batch},
		logger:     logging.NewLoggerWithService("test"),
	}
	return h, batch
}

// assertDivergence checks exactly one projection_divergences row was recorded
// with the expected table/meter/field (Append cols 1,2,3).
func assertDivergence(t *testing.T, batch *captureBatch, table, meter, field string) {
	t.Helper()
	if len(batch.rows) != 1 {
		t.Fatalf("expected exactly 1 divergence record, got %d", len(batch.rows))
	}
	r := batch.rows[0]
	if r[1] != table || r[2] != meter || r[3] != field {
		t.Errorf("divergence = table %v / meter %v / field %v, want %s / %s / %s", r[1], r[2], r[3], table, meter, field)
	}
}

func TestCheckViewerSessionDivergence(t *testing.T) {
	base := viewerSessionFinalRow{
		tenantID: uuid.New(), nodeID: "node-1", sessionID: "sess-1", clusterID: "c1",
		durationSeconds: 100, uploadedBytes: 5000, downloadedBytes: 3000, sourceEventID: "evt-1",
	}
	// Prior projection matching base (no divergence) unless overridden.
	matchingPrior := func() []any { return []any{uint32(100), uint64(5000), uint64(3000), "c1"} }

	t.Run("no prior projection records nothing", func(t *testing.T) {
		h, batch := newDivergenceHandler(nil)
		if err := h.checkViewerSessionDivergence(context.Background(), base); err != nil {
			t.Fatal(err)
		}
		if len(batch.rows) != 0 {
			t.Errorf("first projection must not record a divergence, got %d", len(batch.rows))
		}
	})

	t.Run("identical prior within epsilon records nothing", func(t *testing.T) {
		h, batch := newDivergenceHandler(matchingPrior())
		if err := h.checkViewerSessionDivergence(context.Background(), base); err != nil {
			t.Fatal(err)
		}
		if len(batch.rows) != 0 {
			t.Errorf("matching prior must not record a divergence, got %d", len(batch.rows))
		}
	})

	t.Run("cluster change is recorded", func(t *testing.T) {
		prior := []any{uint32(100), uint64(5000), uint64(3000), "old-cluster"}
		h, batch := newDivergenceHandler(prior)
		if err := h.checkViewerSessionDivergence(context.Background(), base); err != nil {
			t.Fatal(err)
		}
		assertDivergence(t, batch, "viewer_sessions_final", "delivered_minutes", "cluster_id")
	})

	t.Run("duration delta is recorded", func(t *testing.T) {
		row := base
		row.durationSeconds = 250 // prior 100 → delta >= 1
		h, batch := newDivergenceHandler(matchingPrior())
		if err := h.checkViewerSessionDivergence(context.Background(), row); err != nil {
			t.Fatal(err)
		}
		assertDivergence(t, batch, "viewer_sessions_final", "delivered_minutes", "duration_seconds")
	})

	t.Run("sub-epsilon byte drift is ignored", func(t *testing.T) {
		row := base
		row.uploadedBytes = 5000 + 512 // < 1 KiB epsilon
		h, batch := newDivergenceHandler(matchingPrior())
		if err := h.checkViewerSessionDivergence(context.Background(), row); err != nil {
			t.Fatal(err)
		}
		if len(batch.rows) != 0 {
			t.Errorf("byte drift under epsilon must be ignored, got %d records", len(batch.rows))
		}
	})
}

func TestCheckStreamSessionDivergence(t *testing.T) {
	base := streamSessionFinalRow{
		tenantID: uuid.New(), nodeID: "node-1", streamID: uuid.New(), sourceEventID: "evt-1",
		clusterID: "c1", sourceStartedAtMS: 1000, sourceEndedAtMS: 61000, // 60s
	}
	// prior: cluster, started_ms, ended_ms → 60s runtime, same cluster.
	matchingPrior := func() []any { return []any{"c1", int64(1000), int64(61000)} }

	t.Run("no prior records nothing", func(t *testing.T) {
		h, batch := newDivergenceHandler(nil)
		if err := h.checkStreamSessionDivergence(context.Background(), base); err != nil {
			t.Fatal(err)
		}
		if len(batch.rows) != 0 {
			t.Errorf("got %d records, want 0", len(batch.rows))
		}
	})

	t.Run("matching runtime records nothing", func(t *testing.T) {
		h, batch := newDivergenceHandler(matchingPrior())
		if err := h.checkStreamSessionDivergence(context.Background(), base); err != nil {
			t.Fatal(err)
		}
		if len(batch.rows) != 0 {
			t.Errorf("got %d records, want 0", len(batch.rows))
		}
	})

	t.Run("cluster change recorded", func(t *testing.T) {
		prior := []any{"old-cluster", int64(1000), int64(61000)}
		h, batch := newDivergenceHandler(prior)
		if err := h.checkStreamSessionDivergence(context.Background(), base); err != nil {
			t.Fatal(err)
		}
		assertDivergence(t, batch, "stream_sessions_final", "stream_runtime_seconds", "cluster_id")
	})

	t.Run("runtime delta recorded", func(t *testing.T) {
		row := base
		row.sourceEndedAtMS = 121000 // 120s vs prior 60s
		h, batch := newDivergenceHandler(matchingPrior())
		if err := h.checkStreamSessionDivergence(context.Background(), row); err != nil {
			t.Fatal(err)
		}
		assertDivergence(t, batch, "stream_sessions_final", "stream_runtime_seconds", "runtime_seconds")
	})
}

func TestCheckProcessingSegmentDivergence(t *testing.T) {
	base := processingSegmentFinalRow{
		tenantID: uuid.New(), nodeID: "node-1", streamID: uuid.New(),
		processType: "transcode", outputCodec: "h264", trackType: "video",
		sourceEventID: "evt-1", clusterID: "c1", mediaSeconds: 10.0,
	}
	matchingPrior := func() []any { return []any{"transcode", "h264", "video", float64(10.0), "c1"} }

	t.Run("no prior records nothing", func(t *testing.T) {
		h, batch := newDivergenceHandler(nil)
		if err := h.checkProcessingSegmentDivergence(context.Background(), base); err != nil {
			t.Fatal(err)
		}
		if len(batch.rows) != 0 {
			t.Errorf("got %d records, want 0", len(batch.rows))
		}
	})

	t.Run("identity change recorded", func(t *testing.T) {
		row := base
		row.outputCodec = "av1" // attribution corrected → identity divergence
		h, batch := newDivergenceHandler(matchingPrior())
		if err := h.checkProcessingSegmentDivergence(context.Background(), row); err != nil {
			t.Fatal(err)
		}
		assertDivergence(t, batch, "processing_segments_final", "media_seconds", "identity")
	})

	t.Run("media_seconds delta recorded", func(t *testing.T) {
		row := base
		row.mediaSeconds = 10.2 // prior 10.0, delta 0.2 >= 0.05 epsilon
		h, batch := newDivergenceHandler(matchingPrior())
		if err := h.checkProcessingSegmentDivergence(context.Background(), row); err != nil {
			t.Fatal(err)
		}
		assertDivergence(t, batch, "processing_segments_final", "media_seconds", "media_seconds")
	})

	t.Run("sub-epsilon media drift ignored", func(t *testing.T) {
		row := base
		row.mediaSeconds = 10.02 // delta 0.02 < 0.05
		h, batch := newDivergenceHandler(matchingPrior())
		if err := h.checkProcessingSegmentDivergence(context.Background(), row); err != nil {
			t.Fatal(err)
		}
		if len(batch.rows) != 0 {
			t.Errorf("sub-epsilon media drift must be ignored, got %d records", len(batch.rows))
		}
	})
}

package handlers

import (
	"context"
	"encoding/json"
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

func TestProjectionDivergenceOccurrenceIdentity(t *testing.T) {
	first := projectionDivergenceOccurrenceID("viewer_sessions_final", "duration_seconds", `{"session_id":"s1"}`, `720`, "evt-1", 1000)
	replay := projectionDivergenceOccurrenceID("viewer_sessions_final", "duration_seconds", `{"session_id":"s1"}`, `720`, "evt-1", 1000)
	recurrence := projectionDivergenceOccurrenceID("viewer_sessions_final", "duration_seconds", `{"session_id":"s1"}`, `720`, "evt-1", 2000)
	if first == "" || first != replay {
		t.Fatalf("occurrence identity is not replay-stable: %q != %q", first, replay)
	}
	if first == recurrence {
		t.Fatalf("later recurrence collapsed to the prior occurrence %q", first)
	}
}

func TestProjectionDivergenceOccurrenceIsVisibleToLegacyReaders(t *testing.T) {
	encoded, err := projectionDivergenceNaturalKey(`{"cluster_id":"cluster-a","session_id":"s1"}`, "occurrence-a")
	if err != nil {
		t.Fatal(err)
	}
	var key map[string]any
	if err := json.Unmarshal([]byte(encoded), &key); err != nil {
		t.Fatal(err)
	}
	if key["cluster_id"] != "cluster-a" || key["session_id"] != "s1" || key["_correction_occurrence"] != "occurrence-a" {
		t.Fatalf("occurrence-bearing natural key = %#v", key)
	}
}

func TestPlaybackDivergenceKeepsLegacyNaturalKeyIdentity(t *testing.T) {
	h, batch := newDivergenceHandler(nil)
	const naturalKey = `{"cluster_id":"cluster-a","session_id":"s1"}`
	if err := h.recordProjectionDivergence(context.Background(), 1234, 1000, "viewer_sessions_final", "delivered_minutes", "duration_seconds", naturalKey, `600`, `720`, "evt-1"); err != nil {
		t.Fatal(err)
	}
	if len(batch.rows) != 1 || batch.rows[0][4] != naturalKey || batch.rows[0][8] != "" {
		t.Fatalf("playback divergence changed legacy identity: %#v", batch.rows)
	}
}

func TestCheckViewerSessionDivergence(t *testing.T) {
	base := viewerSessionFinalRow{
		tenantID: uuid.New(), nodeID: "node-1", sessionID: "sess-1", clusterID: "c1",
		durationSeconds: 100, uploadedBytes: 5000, downloadedBytes: 3000, sourceEventID: "evt-1",
	}
	// Prior projection matching base (no divergence) unless overridden.
	matchingPrior := func() []any { return []any{uint32(100), uint64(5000), uint64(3000), "c1", int64(1000)} }

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
		prior := []any{uint32(100), uint64(5000), uint64(3000), "old-cluster", int64(1000)}
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

func TestCheckRestreamSessionDivergence(t *testing.T) {
	base := restreamSessionFinalRow{
		tenantID: uuid.New(), nodeID: "node-1", sourceEventID: "restream-1",
		clusterID: "cluster-new", streamID: uuid.New(), targetID: uuid.New(),
		durationMS: 720_000, bytesSent: 4 << 30,
	}

	t.Run("cluster move subsumes simultaneous value changes", func(t *testing.T) {
		h, batch := newDivergenceHandler([]any{uint64(600_000), uint64(3 << 30), "cluster-old", int64(1000)})
		if err := h.checkRestreamSessionDivergence(context.Background(), base); err != nil {
			t.Fatal(err)
		}
		assertDivergence(t, batch, "restream_sessions_final", "delivered_minutes", "cluster_id")
	})

	t.Run("byte-only correction uses canonical meter", func(t *testing.T) {
		h, batch := newDivergenceHandler([]any{base.durationMS, uint64(3 << 30), base.clusterID, int64(1000)})
		if err := h.checkRestreamSessionDivergence(context.Background(), base); err != nil {
			t.Fatal(err)
		}
		assertDivergence(t, batch, "restream_sessions_final", "egress_gb", "bytes_sent")
	})
}

func TestCheckStreamSessionDivergence(t *testing.T) {
	base := streamSessionFinalRow{
		tenantID: uuid.New(), nodeID: "node-1", streamID: uuid.New(), sourceEventID: "evt-1",
		clusterID: "c1", sourceStartedAtMS: 1000, sourceEndedAtMS: 61000, // 60s
	}
	// prior: cluster, started_ms, ended_ms → 60s runtime, same cluster.
	matchingPrior := func() []any { return []any{"c1", int64(1000), int64(61000), int64(1000)} }

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
		prior := []any{"old-cluster", int64(1000), int64(61000), int64(1000)}
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
	matchingPrior := func() []any { return []any{"transcode", "h264", "video", float64(10.0), "c1", int64(1000)} }

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

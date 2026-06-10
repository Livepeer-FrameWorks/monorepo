package jobs

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"testing"
	"time"

	"frameworks/api_balancing/internal/control"
	"frameworks/api_balancing/internal/state"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/mist"
	ipcpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/ipc"
)

// chapterFinalizationDeadline is max(2*chapter_duration, 30m) capped at 24h.
// The clamp is what keeps a tiny chapter from getting an unworkably short
// finalize budget and an hours-long chapter from parking forever.
func TestChapterFinalizationDeadline(t *testing.T) {
	ms := func(d time.Duration) int64 { return d.Milliseconds() }
	tests := []struct {
		name       string
		start, end int64
		want       time.Duration
	}{
		{"short chapter clamps to min", 0, ms(60 * time.Second), chapterFinalizationMinTimeout},
		{"exactly min boundary", 0, ms(15 * time.Minute), chapterFinalizationMinTimeout},
		{"in range is 2x duration", 0, ms(time.Hour), 2 * time.Hour},
		{"long chapter clamps to max", 0, ms(13 * time.Hour), chapterFinalizationMaxTimeout},
		{"zero duration clamps to min", 5000, 5000, chapterFinalizationMinTimeout},
		{"negative duration clamps to min", ms(time.Hour), 0, chapterFinalizationMinTimeout},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := chapterFinalizationDeadline(control.DVRChapterRow{StartMs: tt.start, EndMs: tt.end})
			if got != tt.want {
				t.Errorf("deadline = %v, want %v", got, tt.want)
			}
		})
	}
}

// chapterPlaybackArtifactHash must be deterministic (retries point at the same
// artifact row by construction) and namespaced by the "dvr_chapter:" prefix so
// it can't collide with a bare sha256 of some other id.
func TestChapterPlaybackArtifactHash(t *testing.T) {
	const id = "chapter-abc"
	h1 := chapterPlaybackArtifactHash(id)
	h2 := chapterPlaybackArtifactHash(id)

	if h1 != h2 {
		t.Errorf("not deterministic: %q != %q", h1, h2)
	}
	if len(h1) != 32 {
		t.Errorf("hash length = %d, want 32", len(h1))
	}
	if chapterPlaybackArtifactHash("chapter-xyz") == h1 {
		t.Error("distinct chapter ids must produce distinct hashes")
	}

	// The salt is load-bearing: the hash must not equal a raw sha256 of the
	// chapter id, or a caller hashing the id directly could collide with it.
	raw := sha256.Sum256([]byte(id))
	if h1 == hex.EncodeToString(raw[:])[:32] {
		t.Error("hash must include the dvr_chapter: salt, not a bare sha256(id)")
	}
}

func TestPendingLocalOnly(t *testing.T) {
	withURL := &ipcpb.DVRChapterSegmentRef{PresignedRecoveryUrl: "https://s3/x"}
	noURL := &ipcpb.DVRChapterSegmentRef{}

	tests := []struct {
		name string
		refs []*ipcpb.DVRChapterSegmentRef
		want bool
	}{
		{"empty slice", nil, false},
		{"all have recovery url", []*ipcpb.DVRChapterSegmentRef{withURL, withURL}, false},
		{"one missing url", []*ipcpb.DVRChapterSegmentRef{withURL, noURL}, true},
		{"only missing url", []*ipcpb.DVRChapterSegmentRef{noURL}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := pendingLocalOnly(tt.refs); got != tt.want {
				t.Errorf("pendingLocalOnly = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestBuildSegmentRefs_StatusRouting(t *testing.T) {
	// Fake the S3 signer so we can route by status without a live signer.
	prevPresign := presignArtifactGET
	t.Cleanup(func() { presignArtifactGET = prevPresign })
	presignArtifactGET = func(_ context.Context, key string) (string, error) {
		return "https://s3.example/" + key, nil
	}

	q := &ChapterFinalizationQueue{}
	rows := []control.DVRSegmentRow{
		{SegmentName: "s0", Sequence: 0, Status: "pending", DurationMs: 2000, MediaStartMs: 0, MediaEndMs: 2000, SizeBytes: sql.NullInt64{Int64: 100, Valid: true}},
		{SegmentName: "s1", Sequence: 1, Status: "uploaded", S3Key: "k1"},
		{SegmentName: "s2", Sequence: 2, Status: "deleted_local", S3Key: "k2"},
		{SegmentName: "s3", Sequence: 3, Status: "lost_local", S3Key: "k3"},
		{SegmentName: "s4", Sequence: 4, Status: "lost_local", S3Key: ""}, // missing
		{SegmentName: "s5", Sequence: 5, Status: "reclaimed"},             // missing
	}

	refs, missing, trimmedTail, err := q.buildSegmentRefs("t", "stream", "hash", rows)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if trimmedTail != 0 {
		t.Errorf("trimmedTail = %d, want 0", trimmedTail)
	}
	if len(refs) != len(rows) {
		t.Fatalf("got %d refs, want %d", len(refs), len(rows))
	}
	if missing != 2 {
		t.Errorf("missing = %d, want 2 (lost_local w/o key + reclaimed)", missing)
	}

	// pending: local, no recovery url; field mapping + SizeBytes carried.
	if refs[0].GetPresignedRecoveryUrl() != "" {
		t.Error("pending segment must not get a recovery url")
	}
	if refs[0].DurationMs != 2000 || refs[0].MediaEndMs != 2000 || refs[0].SizeBytes != 100 {
		t.Errorf("pending field mapping wrong: %+v", refs[0])
	}
	// uploaded / deleted_local / lost_local-with-key all get a presigned url.
	for _, i := range []int{1, 2, 3} {
		if refs[i].GetPresignedRecoveryUrl() == "" {
			t.Errorf("ref %d (%s) should have a recovery url", i, rows[i].Status)
		}
	}
	// lost_local without S3Key and reclaimed stay local-less.
	if refs[4].GetPresignedRecoveryUrl() != "" || refs[5].GetPresignedRecoveryUrl() != "" {
		t.Error("lost_local-no-key and reclaimed must not get a recovery url")
	}
}

func TestBuildSegmentRefs_SizeBytesGuard(t *testing.T) {
	q := &ChapterFinalizationQueue{}
	rows := []control.DVRSegmentRow{
		{SegmentName: "valid", Status: "pending", SizeBytes: sql.NullInt64{Int64: 512, Valid: true}},
		{SegmentName: "null", Status: "pending", SizeBytes: sql.NullInt64{Valid: false}},
		{SegmentName: "zero", Status: "pending", SizeBytes: sql.NullInt64{Int64: 0, Valid: true}},
	}
	refs, _, _, err := q.buildSegmentRefs("t", "stream", "hash", rows)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if refs[0].SizeBytes != 512 {
		t.Errorf("valid size = %d, want 512", refs[0].SizeBytes)
	}
	if refs[1].SizeBytes != 0 || refs[2].SizeBytes != 0 {
		t.Errorf("null/zero size should map to 0, got %d/%d", refs[1].SizeBytes, refs[2].SizeBytes)
	}
}

// The highest-value assertion: a transient presign failure must fail the whole
// ref build (so the dispatcher rolls the chapter back to 'closed' for retry).
// It must NOT be swallowed — a signer blip silently downgraded to a local-miss
// would let the chapter be marked failed_source_missing on a transient.
func TestBuildSegmentRefs_PresignErrorIsRetryable(t *testing.T) {
	prevPresign := presignArtifactGET
	t.Cleanup(func() { presignArtifactGET = prevPresign })
	presignArtifactGET = func(context.Context, string) (string, error) {
		return "", errors.New("signer unavailable")
	}

	q := &ChapterFinalizationQueue{}
	rows := []control.DVRSegmentRow{
		{SegmentName: "s1", Status: "uploaded", S3Key: "k1"},
	}
	refs, _, _, err := q.buildSegmentRefs("t", "stream", "hash", rows)
	if err == nil {
		t.Fatal("a presign failure must surface as an error, not a silent local-miss")
	}
	if refs != nil {
		t.Errorf("refs must be nil on presign failure, got %v", refs)
	}
}

func TestBuildSegmentRefs_TrimsLostLocalTail(t *testing.T) {
	prevPresign := presignArtifactGET
	t.Cleanup(func() { presignArtifactGET = prevPresign })
	presigned := 0
	presignArtifactGET = func(_ context.Context, key string) (string, error) {
		presigned++
		return "https://s3.example/" + key, nil
	}

	q := &ChapterFinalizationQueue{}
	rows := []control.DVRSegmentRow{
		{SegmentName: "s0", Sequence: 0, Status: "uploaded", S3Key: "k0", DurationMs: 2000, MediaStartMs: 0, MediaEndMs: 2000},
		{SegmentName: "s1", Sequence: 1, Status: "pending", DurationMs: 2000, MediaStartMs: 2000, MediaEndMs: 4000},
		{SegmentName: "s2", Sequence: 2, Status: "lost_local", S3Key: "k2", DurationMs: 2000, MediaStartMs: 4000, MediaEndMs: 6000},
		{SegmentName: "s3", Sequence: 3, Status: "lost_local", DurationMs: 2000, MediaStartMs: 6000, MediaEndMs: 8000},
	}

	refs, missing, trimmedTail, err := q.buildSegmentRefs("t", "stream", "hash", rows)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if missing != 0 {
		t.Errorf("missing = %d, want 0", missing)
	}
	if trimmedTail != 2 {
		t.Errorf("trimmedTail = %d, want 2", trimmedTail)
	}
	if len(refs) != 2 {
		t.Fatalf("got %d refs, want 2", len(refs))
	}
	if refs[0].GetSegmentName() != "s0" || refs[1].GetSegmentName() != "s1" {
		t.Fatalf("unexpected refs after trim: %+v", refs)
	}
	if presigned != 1 {
		t.Errorf("presigned = %d, want 1; trimmed lost tail must not request recovery URLs", presigned)
	}
}

func TestBuildSegmentRefs_InternalLostLocalStillFails(t *testing.T) {
	prevPresign := presignArtifactGET
	t.Cleanup(func() { presignArtifactGET = prevPresign })
	presignArtifactGET = func(_ context.Context, key string) (string, error) {
		return "https://s3.example/" + key, nil
	}

	q := &ChapterFinalizationQueue{}
	rows := []control.DVRSegmentRow{
		{SegmentName: "s0", Sequence: 0, Status: "pending", DurationMs: 2000, MediaStartMs: 0, MediaEndMs: 2000},
		{SegmentName: "s1", Sequence: 1, Status: "lost_local", DurationMs: 2000, MediaStartMs: 2000, MediaEndMs: 4000},
		{SegmentName: "s2", Sequence: 2, Status: "uploaded", S3Key: "k2", DurationMs: 2000, MediaStartMs: 4000, MediaEndMs: 6000},
	}

	refs, missing, trimmedTail, err := q.buildSegmentRefs("t", "stream", "hash", rows)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if trimmedTail != 0 {
		t.Errorf("trimmedTail = %d, want 0", trimmedTail)
	}
	if missing != 1 {
		t.Errorf("missing = %d, want 1", missing)
	}
	if len(refs) != len(rows) {
		t.Fatalf("got %d refs, want %d", len(refs), len(rows))
	}
}

// nodeAliveAndProcessingCapable mirrors routeProcessingJob eligibility: the
// queue may only treat the recording origin as authoritative when that node is
// alive, healthy, processing-capable, and has a free transcode slot.
func TestNodeAliveAndProcessingCapable(t *testing.T) {
	// These cases seed an alive, processing-capable node into the shared
	// default manager. Leave a fresh empty manager behind (runs last, after
	// every subtest's own cleanup) so a leftover alive node can't leak into
	// later tests in the package — the processing dispatcher selects alive
	// processing-capable nodes and would route to a phantom.
	t.Cleanup(func() { state.ResetDefaultManagerForTests() })

	tests := []struct {
		name  string
		setup func(sm *state.StreamStateManager)
		query string
		want  bool
	}{
		{
			name:  "empty node id",
			setup: func(*state.StreamStateManager) {},
			query: "",
			want:  false,
		},
		{
			name:  "node not present",
			setup: func(*state.StreamStateManager) {},
			query: "node-x",
			want:  false,
		},
		{
			name: "alive but not processing-capable",
			setup: func(sm *state.StreamStateManager) {
				sm.TouchNode("node-1", true)
				setNodeProcessing(sm, "node-1", false, 4, 0)
			},
			query: "node-1",
			want:  false,
		},
		{
			name: "alive, capable, but no free slot",
			setup: func(sm *state.StreamStateManager) {
				sm.TouchNode("node-1", true)
				setNodeProcessing(sm, "node-1", true, 2, 2)
			},
			query: "node-1",
			want:  false,
		},
		{
			name: "unhealthy node is not alive",
			setup: func(sm *state.StreamStateManager) {
				sm.TouchNode("node-1", false)
				setNodeProcessing(sm, "node-1", true, 4, 0)
			},
			query: "node-1",
			want:  false,
		},
		{
			name: "alive, capable, free slot",
			setup: func(sm *state.StreamStateManager) {
				sm.TouchNode("node-1", true)
				setNodeProcessing(sm, "node-1", true, 4, 1)
			},
			query: "node-1",
			want:  true,
		},
		{
			name: "capable with unbounded slots (MaxTranscodes 0)",
			setup: func(sm *state.StreamStateManager) {
				sm.TouchNode("node-1", true)
				setNodeProcessing(sm, "node-1", true, 0, 99)
			},
			query: "node-1",
			want:  true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sm := state.ResetDefaultManagerForTests()
			t.Cleanup(sm.Shutdown)
			tt.setup(sm)
			if got := nodeAliveAndProcessingCapable(tt.query); got != tt.want {
				t.Errorf("nodeAliveAndProcessingCapable(%q) = %v, want %v", tt.query, got, tt.want)
			}
		})
	}
}

// setNodeProcessing sets the processing capability + transcode slot fields the
// eligibility check reads. The metrics struct is anonymous, so the literal is
// repeated here verbatim.
func pushNodeMetrics(sm *state.StreamStateManager, nodeID string, capProcessing bool, classes map[string]state.ClassCapacity) {
	sm.UpdateNodeMetrics(nodeID, struct {
		CPU                  float64
		RAMMax               float64
		RAMCurrent           float64
		UpSpeed              float64
		DownSpeed            float64
		BWLimit              float64
		CapIngest            bool
		CapEdge              bool
		CapStorage           bool
		CapProcessing        bool
		Roles                []string
		StorageCapacityBytes uint64
		StorageUsedBytes     uint64
		ProcessingClasses    map[string]state.ClassCapacity
	}{
		CapProcessing:     capProcessing,
		ProcessingClasses: classes,
	})
}

func setNodeProcessing(sm *state.StreamStateManager, nodeID string, capProcessing bool, maxTranscodes, currentTranscodes int) {
	// A processing-capable node advertises a video_transcode class with the given
	// ceiling (0 = unbounded) and in-flight count; a non-capable node advertises
	// no class at all.
	var classes map[string]state.ClassCapacity
	if capProcessing {
		classes = map[string]state.ClassCapacity{
			mist.ProcessingClassVideoTranscode: {Total: maxTranscodes, Used: currentTranscodes},
		}
	}
	pushNodeMetrics(sm, nodeID, capProcessing, classes)
}

// setNodeClassCapacity advertises an arbitrary set of processing classes on a
// processing-capable node (used to exercise class-aware routing).
func setNodeClassCapacity(sm *state.StreamStateManager, nodeID string, classes map[string]state.ClassCapacity) {
	pushNodeMetrics(sm, nodeID, true, classes)
}

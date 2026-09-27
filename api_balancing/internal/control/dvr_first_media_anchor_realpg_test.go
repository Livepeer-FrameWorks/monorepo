//go:build schema_verify

package control

import (
	"testing"
	"time"
)

// Chapter 1 opens at the first recorded media, to the millisecond. A later
// segment and the recorder's own (earlier, second-precision) start report
// cannot move that anchor.
func TestDVRChapterGridAnchorsAtFirstMedia_RealPG(t *testing.T) {
	conn := startRealPG(t)
	prev := db
	SetDB(conn)
	t.Cleanup(func() { SetDB(prev) })
	const tenant = "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa"
	const hash = "first-media-anchor"
	if _, err := conn.ExecContext(t.Context(), `INSERT INTO foghorn.artifacts
		(artifact_hash, artifact_type, tenant_id, status, dvr_start_dispatch, dvr_chapter_mode, dvr_window_seconds)
		VALUES ($1, 'dvr', $2, 'recording', '{"node_id":"owner"}', $3, 60)`, hash, tenant, ChapterModeWindowSized); err != nil {
		t.Fatal(err)
	}
	recorderStart := time.Now().UTC().Add(-2 * time.Minute).Truncate(time.Second)
	firstMediaMs := recorderStart.UnixMilli() + 2847

	if _, err := InsertDVRSegment(t.Context(), "bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb", hash, "foreign.ts", "k/foreign.ts",
		firstMediaMs-10_000, firstMediaMs-4_000, 6_000, false); err == nil {
		t.Fatal("segment for a foreign tenant was accepted")
	}
	if _, err := InsertDVRSegment(t.Context(), tenant, hash, "seg-0.ts", "k/seg-0.ts",
		firstMediaMs, firstMediaMs+6_000, 6_000, false); err != nil {
		t.Fatal(err)
	}
	if _, err := InsertDVRSegment(t.Context(), tenant, hash, "seg-1.ts", "k/seg-1.ts",
		firstMediaMs+6_000, firstMediaMs+12_000, 6_000, false); err != nil {
		t.Fatal(err)
	}
	if err := recordDVRStartTime(t.Context(), hash, tenant, "owner", recorderStart.Unix()); err != nil {
		t.Fatal(err)
	}

	policy, ok, err := ReadDVRChapterPolicy(t.Context(), hash)
	if err != nil || !ok {
		t.Fatalf("chapter policy unavailable after first segment: ok=%v err=%v", ok, err)
	}
	if policy.StartedAtMs != firstMediaMs {
		t.Fatalf("chapter anchor = %d, want first media %d (off by %d ms)", policy.StartedAtMs, firstMediaMs, firstMediaMs-policy.StartedAtMs)
	}
	startMs, _, ok := CurrentChapterBounds(policy.Mode, policy.EffectiveIntervalSeconds(), policy.StartedAtMs, firstMediaMs+1)
	if !ok || startMs != firstMediaMs {
		t.Fatalf("chapter 1 opens at %d, want %d", startMs, firstMediaMs)
	}
}

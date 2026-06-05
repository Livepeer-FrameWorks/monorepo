package control

import "testing"

// BuildChapterID is the idempotency key for a DVR chapter: OpenChapter is a
// no-op when the same id reappears. So it must be deterministic for identical
// inputs and change when any identifying field changes — otherwise two distinct
// chapters collide (data loss) or the same chapter re-inserts (duplicate).
func TestBuildChapterIDDeterministicAndSensitive(t *testing.T) {
	id1 := BuildChapterID("artifact-1", "interval", 6, 1000, 2000)
	id2 := BuildChapterID("artifact-1", "interval", 6, 1000, 2000)
	if id1 != id2 {
		t.Fatalf("BuildChapterID is not deterministic: %q != %q", id1, id2)
	}
	if len(id1) != 32 {
		t.Fatalf("BuildChapterID length = %d, want 32", len(id1))
	}

	// Sensitivity: changing any field must change the id.
	variants := map[string]string{
		"artifact": BuildChapterID("artifact-2", "interval", 6, 1000, 2000),
		"mode":     BuildChapterID("artifact-1", "boundary", 6, 1000, 2000),
		"interval": BuildChapterID("artifact-1", "interval", 12, 1000, 2000),
		"startMs":  BuildChapterID("artifact-1", "interval", 6, 1001, 2000),
		"endMs":    BuildChapterID("artifact-1", "interval", 6, 1000, 2001),
	}
	seen := map[string]string{id1: "base"}
	for field, id := range variants {
		if prev, dup := seen[id]; dup {
			t.Errorf("changing %s produced same id as %s — chapters would collide", field, prev)
		}
		seen[id] = field
	}
}

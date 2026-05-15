package leases

import (
	"os"
	"path/filepath"
	"sort"
	"testing"
)

func TestSourceRegistry_RecordLookupForget(t *testing.T) {
	r := NewSourceRegistry()
	r.Record(SourceEntry{StreamName: "vod+abc", LocalPath: "/data/vod/abc.mp4", AssetType: "vod", InternalName: "abc"})

	got, ok := r.Lookup("vod+abc")
	if !ok || got.LocalPath != "/data/vod/abc.mp4" {
		t.Fatalf("lookup failed: %+v ok=%v", got, ok)
	}
	if got.InternalName != "abc" {
		t.Fatalf("expected InternalName=abc, got %q", got.InternalName)
	}

	r.Forget("vod+abc")
	if _, ok := r.Lookup("vod+abc"); ok {
		t.Fatalf("expected forgotten entry to be absent")
	}
}

func TestIsLocalFilesystemResponse(t *testing.T) {
	cases := []struct {
		in   string
		want bool
	}{
		{"", false},
		{"/data/vod/abc.mp4", true},
		{"/var/lib/foo", true},
		{"balance:foghorn1", false},
		{"http://node/source", false},
		{"https://s3/bucket/file.mp4", false},
		{"s3://bucket/key", false},
		{"rtmp://host/app/stream", false},
		{"pull+something", false},
		{"//double-slash", false},
		{"relative/path", false},
	}
	for _, c := range cases {
		if got := IsLocalFilesystemResponse(c.in); got != c.want {
			t.Errorf("IsLocalFilesystemResponse(%q)=%v want=%v", c.in, got, c.want)
		}
	}
}

func TestChapterRegistry_RegisterLookup(t *testing.T) {
	r := NewChapterRegistry()
	r.Register(ChapterEntry{
		ChapterID:    "c1",
		DvrHash:      "dvr1",
		SegmentNames: []string{"a.ts", "b.ts"},
		ManifestPath: "/dvr/s/dvr1/chapters/c1.m3u8",
	})

	got, ok := r.Lookup("c1")
	if !ok || got.DvrHash != "dvr1" {
		t.Fatalf("lookup failed: %+v ok=%v", got, ok)
	}
	if len(got.SegmentNames) != 2 || got.SegmentNames[0] != "a.ts" {
		t.Fatalf("segment names not preserved: %+v", got.SegmentNames)
	}

	// Mutating the returned slice should not affect the stored entry.
	got.SegmentNames[0] = "mutated"
	again, _ := r.Lookup("c1")
	if again.SegmentNames[0] != "a.ts" {
		t.Fatalf("registry leaked internal slice; got %q", again.SegmentNames[0])
	}
}

func TestChapterRegistry_RehydrateFromManifests(t *testing.T) {
	root := t.TempDir()
	chaptersDir := filepath.Join(root, "dvr", "streamA", "dvrhash1", "chapters")
	if err := os.MkdirAll(chaptersDir, 0o755); err != nil {
		t.Fatal(err)
	}

	manifest := `#EXTM3U
#EXT-X-VERSION:3
#EXT-X-TARGETDURATION:6
#EXTINF:6.0,
../segments/seg-001.ts
#EXTINF:6.0,
../segments/seg-002.ts
#EXT-X-GAP
#EXTINF:6.0,
../segments/lost.ts
#EXTINF:6.0,
../segments/seg-003.ts
#EXT-X-ENDLIST
`
	if err := os.WriteFile(filepath.Join(chaptersDir, "chap-1.m3u8"), []byte(manifest), 0o644); err != nil {
		t.Fatal(err)
	}

	r := NewChapterRegistry()
	if err := r.Rehydrate(root); err != nil {
		t.Fatalf("rehydrate: %v", err)
	}
	entry, ok := r.Lookup("chap-1")
	if !ok {
		t.Fatalf("expected chap-1 entry, got none")
	}
	if entry.DvrHash != "dvrhash1" {
		t.Errorf("expected DvrHash=dvrhash1, got %q", entry.DvrHash)
	}
	// We don't distinguish gap from non-gap during rehydrate (the manifest's
	// segment URIs are all that's left to read); confirm we picked up the
	// non-comment URIs.
	sort.Strings(entry.SegmentNames)
	want := []string{"lost.ts", "seg-001.ts", "seg-002.ts", "seg-003.ts"}
	if len(entry.SegmentNames) != len(want) {
		t.Fatalf("expected %d segments, got %d (%v)", len(want), len(entry.SegmentNames), entry.SegmentNames)
	}
	for i := range want {
		if entry.SegmentNames[i] != want[i] {
			t.Errorf("segment[%d]=%q want %q", i, entry.SegmentNames[i], want[i])
		}
	}
}

func TestDeriveDvrHashFromPath(t *testing.T) {
	got := DeriveDvrHashFromPath("/storage/dvr/streamA/dvrhash1/chapters/c1.m3u8")
	if got != "dvrhash1" {
		t.Errorf("DeriveDvrHashFromPath returned %q", got)
	}
	if DeriveDvrHashFromPath("/foo/bar") != "" {
		t.Errorf("expected empty derive for malformed path")
	}
}

func TestHeatTracker_TouchAndLookup(t *testing.T) {
	h := NewHeatTracker()
	if _, ok := h.Lookup("/x"); ok {
		t.Fatalf("expected miss on empty tracker")
	}
	h.Touch("/x")
	h.Touch("/x")
	got, ok := h.Lookup("/x")
	if !ok || got.AccessCount != 2 {
		t.Fatalf("expected count=2, got %+v ok=%v", got, ok)
	}
}

func TestDeferredStore_RoundTrip(t *testing.T) {
	dir := t.TempDir()
	deleted := map[string]bool{}
	store := NewDeferredStore(dir, func(assetType, assetHash string) (uint64, error) {
		if deleted[assetType+"|"+assetHash] {
			return 0, nil
		}
		deleted[assetType+"|"+assetHash] = true
		return 100, nil
	}, nil)
	store.Enqueue(PendingDelete{AssetType: "clip", AssetHash: "h1"})
	store.Enqueue(PendingDelete{AssetType: "vod", AssetHash: "h2"})
	if store.Count() != 2 {
		t.Fatalf("expected 2 pending, got %d", store.Count())
	}

	// Persist + reload.
	store2 := NewDeferredStore(dir, nil, nil)
	if err := store2.Load(); err != nil {
		t.Fatalf("load: %v", err)
	}
	if store2.Count() != 2 {
		t.Fatalf("expected 2 pending after reload, got %d", store2.Count())
	}

	// Drain on the original store with the real deleter.
	if n := store.Drain(); n != 2 {
		t.Fatalf("expected 2 successful drains, got %d", n)
	}
	if store.Count() != 0 {
		t.Fatalf("expected empty after drain, got %d", store.Count())
	}
}

func TestDeferredStore_LeaseHeldStaysQueued(t *testing.T) {
	dir := t.TempDir()
	store := NewDeferredStore(dir, func(assetType, assetHash string) (uint64, error) {
		return 0, ErrLeaseHeld
	}, nil)
	store.Enqueue(PendingDelete{AssetType: "clip", AssetHash: "h1"})
	store.Drain()
	if store.Count() != 1 {
		t.Fatalf("expected entry to remain queued under lease-held, got count=%d", store.Count())
	}
}

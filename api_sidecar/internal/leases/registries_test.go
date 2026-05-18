package leases

import (
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
	if err := store.Enqueue(PendingDelete{AssetType: "clip", AssetHash: "h1"}); err != nil {
		t.Fatalf("enqueue clip: %v", err)
	}
	if err := store.Enqueue(PendingDelete{AssetType: "vod", AssetHash: "h2"}); err != nil {
		t.Fatalf("enqueue vod: %v", err)
	}
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
	if err := store.Enqueue(PendingDelete{AssetType: "clip", AssetHash: "h1"}); err != nil {
		t.Fatalf("enqueue clip: %v", err)
	}
	store.Drain()
	if store.Count() != 1 {
		t.Fatalf("expected entry to remain queued under lease-held, got count=%d", store.Count())
	}
}

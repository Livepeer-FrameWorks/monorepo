package control

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"frameworks/api_sidecar/internal/storage"
	ipcpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/ipc"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/mist"
)

func TestURLEscape(t *testing.T) {
	cases := map[string]string{
		"":              "",
		"plain":         "plain",
		"a b":           "a%20b",
		"  ":            "%20%20",
		"live+stream 1": "live+stream%201",
	}
	for in, want := range cases {
		if got := urlEscape(in); got != want {
			t.Errorf("urlEscape(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestRelayBaseURL(t *testing.T) {
	t.Run("falls back to colocated loopback when env unset", func(t *testing.T) {
		t.Setenv("HELMSMAN_RELAY_BASE_URL", "")
		if got := relayBaseURL(); got != "http://127.0.0.1:18007" {
			t.Errorf("relayBaseURL() = %q, want loopback default", got)
		}
	})
	t.Run("honours env and trims trailing slash", func(t *testing.T) {
		t.Setenv("HELMSMAN_RELAY_BASE_URL", "http://helmsman:18007/")
		if got := relayBaseURL(); got != "http://helmsman:18007" {
			t.Errorf("relayBaseURL() = %q, want trimmed service URL", got)
		}
	})
}

// SegmentInRollingManifest is the third clause of the eviction predicate: a
// segment still advertised by the live playlist must never be reported as
// safe to delete.
func TestSegmentInRollingManifest(t *testing.T) {
	dir := t.TempDir()
	manifestPath := filepath.Join(dir, "rolling.m3u8")
	manifest := "#EXTM3U\n#EXTINF:6.0,\nsegments/seg-0001.ts\n#EXTINF:6.0,\nseg-0002.ts\n"
	if err := os.WriteFile(manifestPath, []byte(manifest), 0o644); err != nil {
		t.Fatal(err)
	}
	job := &DVRJob{ManifestPath: manifestPath}

	cases := []struct {
		name    string
		job     *DVRJob
		segment string
		want    bool
	}{
		{"nil job", nil, "seg-0001.ts", false},
		{"empty manifest path", &DVRJob{}, "seg-0001.ts", false},
		{"empty segment name", job, "", false},
		{"segment referenced via path prefix", job, "seg-0001.ts", true},
		{"segment referenced via bare line", job, "seg-0002.ts", true},
		{"segment absent", job, "seg-9999.ts", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := SegmentInRollingManifest(tc.job, tc.segment); got != tc.want {
				t.Errorf("SegmentInRollingManifest = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestSanitizeDvrStorageError(t *testing.T) {
	spaceErr := fmt.Errorf("upload failed: %w", storage.ErrInsufficientSpace)
	if got := sanitizeDvrStorageError(spaceErr); got != "Recording stopped: storage node out of space" {
		t.Errorf("space error -> %q", got)
	}
	if got := sanitizeDvrStorageError(fmt.Errorf("connection reset")); got != "Recording stopped: storage error" {
		t.Errorf("generic error -> %q (must not leak detail)", got)
	}
}

func TestFindDVRPush(t *testing.T) {
	pushes := []mist.PushInfo{
		{ID: 1, TargetURI: "s3://bucket/other/seg.ts"},
		{ID: 2, TargetURI: "dtsc://node/dvr+abc123/seg.ts"},
		{ID: 3, ActualURI: "https://peer/relay/def456/manifest.m3u8"},
	}
	if got, ok := findDVRPush(pushes, "abc123"); !ok || got.ID != 2 {
		t.Errorf("match in TargetURI: got=%+v ok=%v", got, ok)
	}
	if got, ok := findDVRPush(pushes, "def456"); !ok || got.ID != 3 {
		t.Errorf("match in ActualURI: got=%+v ok=%v", got, ok)
	}
	if _, ok := findDVRPush(pushes, "missing"); ok {
		t.Error("unexpected match for absent hash")
	}
	if _, ok := findDVRPush(nil, "abc123"); ok {
		t.Error("unexpected match in empty push list")
	}
}

func TestLocalSegmentIndexForgetAndLocalPath(t *testing.T) {
	idx := newTestSegmentIndex()
	idx.entries[localSegmentKey{"dvr1", "seg-0001.ts"}] = &LocalSegmentRef{LocalPath: "/data/dvr/dvr1/segments/seg-0001.ts"}
	idx.entries[localSegmentKey{"dvr1", "seg-0002.ts"}] = &LocalSegmentRef{LocalPath: ""}

	if p, ok := idx.LocalPath("dvr1", "seg-0001.ts"); !ok || p != "/data/dvr/dvr1/segments/seg-0001.ts" {
		t.Errorf("LocalPath present = (%q,%v)", p, ok)
	}
	if _, ok := idx.LocalPath("dvr1", "seg-0002.ts"); ok {
		t.Error("LocalPath with empty stored path must report not-found")
	}
	if _, ok := idx.LocalPath("dvr1", "missing"); ok {
		t.Error("LocalPath for unknown segment must report not-found")
	}

	idx.Forget("dvr1", "seg-0001.ts")
	if _, ok := idx.LocalPath("dvr1", "seg-0001.ts"); ok {
		t.Error("Forget must remove the entry")
	}
	// Idempotent and nil-safe.
	idx.Forget("dvr1", "seg-0001.ts")
	var nilIdx *LocalSegmentIndex
	nilIdx.Forget("dvr1", "seg-0001.ts")
	if _, ok := nilIdx.LocalPath("dvr1", "seg-0001.ts"); ok {
		t.Error("nil index LocalPath must report not-found")
	}
}

// The WAL-depth wrappers gate the /internal/triggers/wal inspection surface and
// the "is anything stuck?" Grafana signal. They must surface the unready error
// before the WAL is opened and proxy to the WAL once it is.
func TestTriggerWALDepthWrappers(t *testing.T) {
	prev := triggerWAL
	triggerWAL = nil
	t.Cleanup(func() { triggerWAL = prev })

	if _, err := TriggerWALPendingDepth(); !errors.Is(err, errTriggerForwarderUnready) {
		t.Errorf("PendingDepth unready err = %v", err)
	}
	if _, err := ListTriggerWALPending(); !errors.Is(err, errTriggerForwarderUnready) {
		t.Errorf("ListPending unready err = %v", err)
	}

	wal, err := storage.NewTriggerWAL(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	triggerWAL = wal

	depth, err := TriggerWALPendingDepth()
	if err != nil || depth != 0 {
		t.Fatalf("empty WAL depth = (%d,%v)", depth, err)
	}
	if _, appendErr := wal.Append(&ipcpb.MistTrigger{RequestId: "evt-1", TriggerType: "PUSH_END"}); appendErr != nil {
		t.Fatal(appendErr)
	}
	depth, err = TriggerWALPendingDepth()
	if err != nil || depth != 1 {
		t.Fatalf("after append depth = (%d,%v)", depth, err)
	}
	pending, err := ListTriggerWALPending()
	if err != nil || len(pending) != 1 || pending[0].GetRequestId() != "evt-1" {
		t.Fatalf("ListPending = (%+v,%v)", pending, err)
	}
}

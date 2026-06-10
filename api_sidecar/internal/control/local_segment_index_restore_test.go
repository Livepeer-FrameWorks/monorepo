package control

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	ipcpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/ipc"
)

// LocalSegmentIndexInstance is the lazily-built process-global cache. First
// call constructs it; later calls return the same instance.
func TestLocalSegmentIndexInstanceLazySingleton(t *testing.T) {
	localSegmentIndexMu.Lock()
	saved := localSegmentIndex
	localSegmentIndex = nil
	localSegmentIndexMu.Unlock()
	t.Cleanup(func() {
		localSegmentIndexMu.Lock()
		localSegmentIndex = saved
		localSegmentIndexMu.Unlock()
	})

	first := LocalSegmentIndexInstance(logging.NewLogger())
	if first == nil {
		t.Fatal("instance must not be nil")
	}
	if second := LocalSegmentIndexInstance(logging.NewLogger()); second != first {
		t.Fatal("expected the same singleton on subsequent calls")
	}
}

func TestMarkUploaded(t *testing.T) {
	idx := newTestSegmentIndex()
	idx.MarkUploaded("dvr-1", "seg-1", "/data/seg-1.ts", 4096)

	ref, ok := idx.testRef("dvr-1", "seg-1")
	if !ok {
		t.Fatal("entry missing after MarkUploaded")
	}
	if !ref.Uploaded || ref.LedgerStatus != "uploaded" || ref.SizeBytes != 4096 {
		t.Fatalf("segment not marked uploaded: %+v", ref)
	}
	// MarkUploaded is the recorder upload path, so it claims the active set.
	if !ref.ActiveRecording {
		t.Fatal("MarkUploaded should mark the segment as active-recording")
	}

	// Nil receiver is a tolerated no-op (index may not be initialized yet).
	var nilIdx *LocalSegmentIndex
	nilIdx.MarkUploaded("dvr-1", "seg-1", "/p", 1)
}

func TestRestoreFromDiskNilAndMissingRoot(t *testing.T) {
	var nilIdx *LocalSegmentIndex
	if err := nilIdx.RestoreFromDisk(context.Background(), t.TempDir()); err == nil {
		t.Fatal("nil index must error")
	}

	// A base path with no dvr/ subtree is not an error — nothing to restore.
	idx := newTestSegmentIndex()
	if err := idx.RestoreFromDisk(context.Background(), t.TempDir()); err != nil {
		t.Fatalf("missing dvr root should be a no-op, got %v", err)
	}
}

// RestoreFromDisk walks dvr/<stream>/<hash>/segments/, asks Foghorn for ledger
// state, and rebuilds the index: segments the ledger knows get its status;
// on-disk files the ledger doesn't return are inserted as orphans (empty
// status) so the eviction sweep can reclaim them. Dotfiles are skipped.
func TestRestoreFromDiskRebuildsIndex(t *testing.T) {
	base := t.TempDir()
	segDir := filepath.Join(base, "dvr", "live+stream-1", "dvr-1", "segments")
	if err := os.MkdirAll(segDir, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"seg-1.ts", "seg-2.ts", ".hidden"} {
		if err := os.WriteFile(filepath.Join(segDir, name), []byte("data"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	stream := connectFake(t)
	idx := &LocalSegmentIndex{
		entries: map[localSegmentKey]*LocalSegmentRef{},
		logger:  logging.NewLogger(),
	}

	done := make(chan struct{})
	var err error
	go func() {
		defer close(done)
		err = idx.RestoreFromDisk(context.Background(), base)
	}()

	sent := waitForControlMessage(t, stream.sendCh, "restore local segment index request")
	req := sent.GetRestoreLocalSegmentIndexRequest()
	if req == nil || req.GetDvrHash() != "dvr-1" {
		t.Fatalf("unexpected restore request: %+v", req)
	}
	if len(req.GetSegmentNames()) != 2 {
		t.Fatalf("dotfile should be skipped; got names %v", req.GetSegmentNames())
	}

	// Ledger knows seg-1 (uploaded); seg-2 is absent → orphan on disk.
	handleRestoreLocalSegmentIndexResponse(&ipcpb.RestoreLocalSegmentIndexResponse{
		RequestId: req.GetRequestId(),
		DvrHash:   "dvr-1",
		Segments: []*ipcpb.DVRSegmentRef{
			{SegmentName: "seg-1.ts", Status: "uploaded"},
		},
	})

	waitForTestDone(t, done, "restore from disk")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	seg1, ok := idx.testRef("dvr-1", "seg-1.ts")
	if !ok || seg1.LedgerStatus != "uploaded" || !seg1.Uploaded {
		t.Fatalf("seg-1 not restored from ledger: %+v", seg1)
	}
	if seg1.LocalPath != filepath.Join(segDir, "seg-1.ts") || seg1.SizeBytes != 4 {
		t.Fatalf("seg-1 disk facts not captured: %+v", seg1)
	}
	seg2, ok := idx.testRef("dvr-1", "seg-2.ts")
	if !ok || seg2.LedgerStatus != "" || seg2.Uploaded {
		t.Fatalf("orphan seg-2 should be inserted with empty status: %+v", seg2)
	}
	if _, ok := idx.testRef("dvr-1", ".hidden"); ok {
		t.Fatal("dotfile must not be indexed")
	}
}

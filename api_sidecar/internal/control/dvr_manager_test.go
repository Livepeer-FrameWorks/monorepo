package control

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"frameworks/pkg/logging"
)

func TestGetActiveDVRHashes_Empty(t *testing.T) {
	prevDM := dvrManager
	dm := &DVRManager{
		logger:      logging.NewLogger(),
		jobs:        make(map[string]*DVRJob),
		storagePath: t.TempDir(),
	}
	dvrManager = dm
	t.Cleanup(func() { dvrManager = prevDM })

	hashes := GetActiveDVRHashes()
	if len(hashes) != 0 {
		t.Fatalf("expected empty map, got %d entries", len(hashes))
	}
}

func TestGetActiveDVRHashes_WithJobs(t *testing.T) {
	prevDM := dvrManager
	dm := &DVRManager{
		logger:      logging.NewLogger(),
		jobs:        make(map[string]*DVRJob),
		storagePath: t.TempDir(),
	}
	dm.jobs["hash-aaa"] = &DVRJob{DVRHash: "hash-aaa", Status: "recording"}
	dm.jobs["hash-bbb"] = &DVRJob{DVRHash: "hash-bbb", Status: "starting"}
	dvrManager = dm
	t.Cleanup(func() { dvrManager = prevDM })

	hashes := GetActiveDVRHashes()
	if len(hashes) != 2 {
		t.Fatalf("expected 2 hashes, got %d", len(hashes))
	}
	if !hashes["hash-aaa"] {
		t.Fatal("expected hash-aaa in result")
	}
	if !hashes["hash-bbb"] {
		t.Fatal("expected hash-bbb in result")
	}
}

func TestHandleNewSegment_PathTraversal(t *testing.T) {
	dm := &DVRManager{
		logger: logging.NewLogger(),
		jobs:   make(map[string]*DVRJob),
	}
	dm.jobs["hash-1"] = &DVRJob{
		DVRHash:        "hash-1",
		StreamName:     "live+test-stream",
		OutputDir:      "/data/dvr/stream-1/hash-1",
		SyncedSegments: make(map[string]bool),
		Logger:         logging.NewLogger(),
	}

	// filePath outside OutputDir should be rejected before any sync attempt
	dm.HandleNewSegment("live+test-stream", "/other/path/segment.ts")

	if len(dm.jobs["hash-1"].SyncedSegments) != 0 {
		t.Fatal("expected no segments synced after path traversal attempt")
	}
}

func TestHandleNewSegment_UnknownStream(t *testing.T) {
	dm := &DVRManager{
		logger: logging.NewLogger(),
		jobs:   make(map[string]*DVRJob),
	}
	dm.jobs["hash-1"] = &DVRJob{
		DVRHash:        "hash-1",
		StreamName:     "live+test-stream",
		OutputDir:      "/data/dvr/stream-1/hash-1",
		SyncedSegments: make(map[string]bool),
		Logger:         logging.NewLogger(),
	}

	// Non-matching stream name should be a no-op without panicking
	dm.HandleNewSegment("live+unknown-stream", "/data/dvr/stream-1/hash-1/segments/chunk000.ts")

	if len(dm.jobs["hash-1"].SyncedSegments) != 0 {
		t.Fatal("expected no segments synced for unknown stream")
	}
}

func TestStopRecording_NotFound(t *testing.T) {
	dm := &DVRManager{
		logger: logging.NewLogger(),
		jobs:   make(map[string]*DVRJob),
	}

	err := dm.StopRecording("nonexistent-hash")
	if err == nil {
		t.Fatal("expected error for unknown DVR hash")
	}
	if !strings.Contains(err.Error(), "not found") {
		t.Fatalf("expected error to contain 'not found', got: %s", err.Error())
	}
}

func TestParseManifestSegments(t *testing.T) {
	content := `#EXTM3U
#EXT-X-VERSION:3
#EXT-X-TARGETDURATION:6
#EXTINF:6.000,
segments/chunk000.ts
#EXTINF:5.500,
segments/chunk001.ts
#EXTINF:4.200,
segments/chunk002.ts?token=abc
#EXT-X-ENDLIST`

	tmpDir := t.TempDir()
	manifestPath := filepath.Join(tmpDir, "test.m3u8")
	if err := os.WriteFile(manifestPath, []byte(content), 0644); err != nil {
		t.Fatalf("failed to write manifest: %v", err)
	}

	dm := &DVRManager{logger: logging.NewLogger()}
	segments, err := dm.parseManifestSegments(manifestPath)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	expected := []string{"chunk000.ts", "chunk001.ts", "chunk002.ts"}
	if len(segments) != len(expected) {
		t.Fatalf("expected %d segments, got %d: %v", len(expected), len(segments), segments)
	}
	for i, seg := range segments {
		if seg != expected[i] {
			t.Fatalf("segment %d: expected %q, got %q", i, expected[i], seg)
		}
	}
}

func TestCalculateRetryDelay(t *testing.T) {
	dm := &DVRManager{logger: logging.NewLogger()}

	cases := []struct {
		retryCount int
		want       time.Duration
	}{
		{0, 5 * time.Second},
		{1, 10 * time.Second},
		{2, 20 * time.Second},
		{3, 40 * time.Second},
		{4, 60 * time.Second},  // capped at MaxRetryDelay
		{5, 60 * time.Second},  // still capped
		{10, 60 * time.Second}, // still capped
	}

	for _, tc := range cases {
		got := dm.calculateRetryDelay(tc.retryCount)
		if got != tc.want {
			t.Errorf("calculateRetryDelay(%d) = %v, want %v", tc.retryCount, got, tc.want)
		}
	}
}

func TestGetActiveJobs(t *testing.T) {
	dm := &DVRManager{
		logger: logging.NewLogger(),
		jobs:   make(map[string]*DVRJob),
	}
	dm.jobs["hash-1"] = &DVRJob{DVRHash: "hash-1", Status: "recording"}
	dm.jobs["hash-2"] = &DVRJob{DVRHash: "hash-2", Status: "starting"}
	dm.jobs["hash-3"] = &DVRJob{DVRHash: "hash-3", Status: "stopped"}

	result := dm.GetActiveJobs()
	if len(result) != 3 {
		t.Fatalf("expected 3 jobs, got %d", len(result))
	}

	expectations := map[string]string{
		"hash-1": "recording",
		"hash-2": "starting",
		"hash-3": "stopped",
	}
	for hash, wantStatus := range expectations {
		gotStatus, ok := result[hash]
		if !ok {
			t.Fatalf("missing hash %s in result", hash)
		}
		if gotStatus != wantStatus {
			t.Fatalf("hash %s: expected status %q, got %q", hash, wantStatus, gotStatus)
		}
	}
}

func TestSyncSpecificSegment_AlreadySynced(t *testing.T) {
	storeConn(&fakeControlStream{}, "dvr-node")
	t.Cleanup(func() { clearConn() })

	job := &DVRJob{
		DVRHash:        "hash-sync",
		StreamName:     "live+test",
		OutputDir:      t.TempDir(),
		SyncedSegments: map[string]bool{"chunk000.ts": true},
		Logger:         logging.NewLogger(),
	}

	dm := &DVRManager{logger: logging.NewLogger()}

	// syncSpecificSegment should return early for already-synced segment
	// without calling RequestFreezePermission (which would fail)
	dm.syncSpecificSegment(job, filepath.Join(job.OutputDir, "segments", "chunk000.ts"))

	if len(job.SyncedSegments) != 1 {
		t.Fatalf("expected SyncedSegments to be unchanged, got %v", job.SyncedSegments)
	}
}

func TestStartRecording_AlreadyActive(t *testing.T) {
	dm := &DVRManager{
		logger:      logging.NewLogger(),
		jobs:        make(map[string]*DVRJob),
		storagePath: t.TempDir(),
	}
	dm.jobs["hash-dup"] = &DVRJob{DVRHash: "hash-dup", Status: "recording"}

	err := dm.StartRecording("hash-dup", "stream-1", "internal-1", "http://source", nil, nil)
	if err == nil {
		t.Fatal("expected error for duplicate hash")
	}
	if !strings.Contains(err.Error(), "already active") {
		t.Fatalf("expected 'already active' error, got: %s", err.Error())
	}
}

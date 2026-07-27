package handlers

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"frameworks/api_sidecar/internal/storage"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	ipcpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/ipc"
)

type fakePresignedClient struct {
	uploadFileCalls  int64
	uploadCalls      int64
	downloadCalls    int64
	lastUploadedPath string
	uploadFileErr    error // when set, UploadFileToPresignedURL fails
}

func (f *fakePresignedClient) UploadFileToPresignedURL(_ context.Context, _, localPath string, onProgress storage.ProgressCallback) error {
	atomic.AddInt64(&f.uploadFileCalls, 1)
	f.lastUploadedPath = localPath
	if f.uploadFileErr != nil {
		return f.uploadFileErr
	}
	if onProgress != nil {
		onProgress(100)
	}
	return nil
}

func (f *fakePresignedClient) UploadToPresignedURL(_ context.Context, _ string, _ io.Reader, _ int64, _ storage.ProgressCallback) error {
	atomic.AddInt64(&f.uploadCalls, 1)
	return nil
}

func (f *fakePresignedClient) DownloadToFileFromPresignedURL(_ context.Context, _, _ string, _ storage.ProgressCallback) error {
	atomic.AddInt64(&f.downloadCalls, 1)
	return nil
}

func (f *fakePresignedClient) DownloadFromPresignedURL(_ context.Context, _ string, _ io.Writer, _ storage.ProgressCallback) (int64, error) {
	atomic.AddInt64(&f.downloadCalls, 1)
	return 0, nil
}

func newTestStorageManager(t *testing.T) *StorageManager {
	t.Helper()
	sm := &StorageManager{
		logger:   logging.NewLogger(),
		basePath: t.TempDir(),

		requestFreezePermission: func(_ context.Context, _, _ string, _ uint64) (*ipcpb.FreezePermissionResponse, error) {
			return nil, fmt.Errorf("not connected")
		},
		sendSyncComplete:     func(_, _, _ string, _ uint64, _ string, _ bool, _ bool) error { return nil },
		sendFreezeProgress:   func(_, _ string, _ uint32, _ uint64) error { return nil },
		sendStorageLifecycle: func(_ *ipcpb.StorageLifecycleData) error { return nil },
		requestCanDelete:     func(_ context.Context, _ string) (bool, string, int64, error) { return false, "", 0, nil },
		sendArtifactDeleted:  func(_, _, _, _ string, _ uint64) error { return nil },
		// No active DVRs by default; tests that exercise the DVR reclaim stage override these.
		activeDVRHashes:    func() map[string]bool { return nil },
		evictDVRSegmentsFn: func(_ string, _ uint64) (int, uint64) { return 0, 0 },
	}
	sm.freezeTracker.inFlight = make(map[string]bool)
	return sm
}

func TestHandleFreezeRequest_FileNotFound(t *testing.T) {
	sm := newTestStorageManager(t)

	req := &ipcpb.FreezeRequest{
		RequestId: "req-1",
		AssetHash: "hash-1",
		AssetType: "clip",
		LocalPath: "/nonexistent/path/clip.mp4",
	}

	// Should not panic; SendSyncComplete will fail silently (no stream)
	sm.HandleFreezeRequest(req)
}

func TestHandleFreezeRequest_DVRNudge(t *testing.T) {
	sm := newTestStorageManager(t)

	// Create a DVR directory with a manifest
	dvrDir := filepath.Join(sm.basePath, "dvr", "stream-1", "hash-dvr")
	if err := os.MkdirAll(dvrDir, 0755); err != nil {
		t.Fatal(err)
	}
	manifest := "#EXTM3U\n#EXT-X-TARGETDURATION:6\n#EXTINF:6.000,\nseg0.ts\n"
	if err := os.WriteFile(filepath.Join(dvrDir, "hash-dvr.m3u8"), []byte(manifest), 0644); err != nil {
		t.Fatal(err)
	}

	req := &ipcpb.FreezeRequest{
		RequestId:    "req-dvr",
		AssetHash:    "hash-dvr",
		AssetType:    "dvr",
		LocalPath:    dvrDir,
		InternalName: "stream-1",
		SegmentUrls:  nil, // Empty → DVR nudge path (falls through to freezeAsset)
	}

	// freezeAsset will call control.RequestFreezePermission which will fail
	// (no gRPC stream). That's fine — we're testing that the nudge path
	// doesn't panic and correctly detects the DVR nudge condition.
	sm.HandleFreezeRequest(req)
}

func TestUploadAsset_ClipNoURL(t *testing.T) {
	sm := newTestStorageManager(t)

	// Create a temp clip file
	clipPath := filepath.Join(sm.basePath, "clip.mp4")
	if err := os.WriteFile(clipPath, []byte("fake clip data"), 0644); err != nil {
		t.Fatal(err)
	}

	asset := FreezeCandidate{
		AssetType: AssetTypeClip,
		AssetHash: "hash-clip",
		FilePath:  clipPath,
		SizeBytes: 14,
	}

	permResp := &ipcpb.FreezePermissionResponse{
		RequestId:       "req-1",
		AssetHash:       "hash-clip",
		Approved:        true,
		PresignedPutUrl: "", // No URL
	}

	ctx := context.Background()
	err := sm.uploadAsset(ctx, asset, permResp)
	if err == nil {
		t.Fatal("expected error for missing presigned URL")
	}
	if got := err.Error(); got != "no presigned URL provided for clip freeze" {
		t.Fatalf("unexpected error: %s", got)
	}
}

func TestUploadAsset_VodNoURL(t *testing.T) {
	sm := newTestStorageManager(t)

	vodPath := filepath.Join(sm.basePath, "output.mkv")
	if err := os.WriteFile(vodPath, []byte("fake vod data"), 0644); err != nil {
		t.Fatal(err)
	}

	asset := FreezeCandidate{
		AssetType: AssetTypeVOD,
		AssetHash: "hash-vod",
		FilePath:  vodPath,
		SizeBytes: 13,
	}

	permResp := &ipcpb.FreezePermissionResponse{
		RequestId:       "req-2",
		AssetHash:       "hash-vod",
		Approved:        true,
		PresignedPutUrl: "",
	}

	ctx := context.Background()
	err := sm.uploadAsset(ctx, asset, permResp)
	if err == nil {
		t.Fatal("expected error for missing presigned URL")
	}
	if got := err.Error(); got != "no presigned URL provided for vod freeze" {
		t.Fatalf("unexpected error: %s", got)
	}
}

func TestUploadAsset_DVRNoSegmentURLs(t *testing.T) {
	sm := newTestStorageManager(t)

	asset := FreezeCandidate{
		AssetType: AssetTypeDVR,
		AssetHash: "hash-dvr",
		FilePath:  sm.basePath,
		SizeBytes: 1024,
	}

	permResp := &ipcpb.FreezePermissionResponse{
		RequestId: "req-3",
		AssetHash: "hash-dvr",
		Approved:  true,
	}

	ctx := context.Background()
	err := sm.uploadAsset(ctx, asset, permResp)
	if err == nil {
		t.Fatal("expected error for missing DVR segment URLs")
	}
	if got := err.Error(); got != "whole-DVR upload is unsupported; DVR archive playlists are generated by Foghorn chapters" {
		t.Fatalf("unexpected error: %s", got)
	}
}

func TestUploadAsset_UnsupportedType(t *testing.T) {
	sm := newTestStorageManager(t)

	asset := FreezeCandidate{
		AssetType: "unknown",
		AssetHash: "hash-x",
		FilePath:  sm.basePath,
		SizeBytes: 100,
	}

	permResp := &ipcpb.FreezePermissionResponse{
		RequestId: "req-4",
		Approved:  true,
	}

	ctx := context.Background()
	err := sm.uploadAsset(ctx, asset, permResp)
	if err == nil {
		t.Fatal("expected error for unsupported asset type")
	}
	if got := err.Error(); got != "unsupported asset type for freeze: unknown" {
		t.Fatalf("unexpected error: %s", got)
	}
}

func TestFreezeTrackerCleansUpOnExit(t *testing.T) {
	sm := newTestStorageManager(t)

	asset := FreezeCandidate{
		AssetType: "unknown",
		AssetHash: "hash-track",
		SizeBytes: 100,
	}

	permResp := &ipcpb.FreezePermissionResponse{
		RequestId: "req-6",
		Approved:  true,
	}

	ctx := context.Background()
	_ = sm.uploadAsset(ctx, asset, permResp)

	// Verify freeze tracker is cleaned up after uploadAsset returns
	sm.freezeTracker.mu.RLock()
	_, tracked := sm.freezeTracker.inFlight["hash-track"]
	sm.freezeTracker.mu.RUnlock()

	if tracked {
		t.Fatal("expected freeze tracker to clean up after uploadAsset completes")
	}
}

func TestSyncDtshOnlyVodRegeneratesInvalidSidecar(t *testing.T) {
	sm := newTestStorageManager(t)
	fake := &fakePresignedClient{}
	sm.presignedClient = fake

	vodPath := filepath.Join(sm.basePath, "artifact123.mkv")
	dtshPath := vodPath + ".dtsh"
	if err := os.WriteFile(vodPath, []byte("vod data"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dtshPath, []byte{'D', 'T', 'S', 'C', 0, 0, 0, 100, '{'}, 0o644); err != nil {
		t.Fatal(err)
	}

	var gotPath string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		_ = os.WriteFile(dtshPath, validDTSHBytes(), 0o644)
		_, _ = w.Write([]byte(`{"meta":{"tracks":{}}}`))
	}))
	t.Cleanup(server.Close)
	t.Setenv("MISTSERVER_URL", server.URL)

	err := sm.SyncDtshOnly(context.Background(), &ipcpb.DtshSyncRequest{
		RequestId:       "req-dtsh",
		AssetType:       "vod",
		AssetHash:       "artifact123",
		LocalPath:       vodPath,
		PresignedPutUrl: "https://s3.example.com/artifact123.mkv.dtsh?presigned",
	})
	if err != nil {
		t.Fatalf("SyncDtshOnly returned error: %v", err)
	}
	if gotPath != "/json_vod+artifact123.js" {
		t.Fatalf("Mist path = %q, want DTSH json endpoint", gotPath)
	}
	if atomic.LoadInt64(&fake.uploadFileCalls) != 1 {
		t.Fatalf("expected 1 upload call, got %d", atomic.LoadInt64(&fake.uploadFileCalls))
	}
	if fake.lastUploadedPath != dtshPath {
		t.Fatalf("uploaded path = %q, want %q", fake.lastUploadedPath, dtshPath)
	}
}

func TestHandleFreezeRequest_ClipUpload(t *testing.T) {
	sm := newTestStorageManager(t)
	fake := &fakePresignedClient{}
	sm.presignedClient = fake

	var syncStatus string
	sm.sendSyncComplete = func(_, _, status string, _ uint64, _ string, _ bool, _ bool) error {
		syncStatus = status
		return nil
	}

	clipPath := filepath.Join(sm.basePath, "clip.mp4")
	if err := os.WriteFile(clipPath, []byte("clip data here!"), 0644); err != nil {
		t.Fatal(err)
	}

	req := &ipcpb.FreezeRequest{
		RequestId:       "req-clip",
		AssetHash:       "hash-clip",
		AssetType:       "clip",
		LocalPath:       clipPath,
		PresignedPutUrl: "https://s3.example.com/clip.mp4?presigned",
	}

	sm.HandleFreezeRequest(req)

	if atomic.LoadInt64(&fake.uploadFileCalls) != 1 {
		t.Fatalf("expected 1 upload call, got %d", atomic.LoadInt64(&fake.uploadFileCalls))
	}
	if syncStatus != "success" {
		t.Fatalf("expected sync status 'success', got %q", syncStatus)
	}
}

func TestHandleFreezeRequest_DVRWithSegments(t *testing.T) {
	sm := newTestStorageManager(t)
	fake := &fakePresignedClient{}
	sm.presignedClient = fake

	var syncStatus string
	sm.sendSyncComplete = func(_, _, status string, _ uint64, _ string, _ bool, _ bool) error {
		syncStatus = status
		return nil
	}

	dvrDir := filepath.Join(sm.basePath, "dvr-upload")
	segDir := filepath.Join(dvrDir, "segments")
	if err := os.MkdirAll(segDir, 0755); err != nil {
		t.Fatal(err)
	}

	manifest := "#EXTM3U\n#EXT-X-VERSION:3\n#EXT-X-TARGETDURATION:6\n#EXTINF:6.000,\nsegments/chunk000.ts\n#EXTINF:5.500,\nsegments/chunk001.ts\n"
	if err := os.WriteFile(filepath.Join(dvrDir, "dvr-hash.m3u8"), []byte(manifest), 0644); err != nil {
		t.Fatal(err)
	}
	for _, seg := range []string{"chunk000.ts", "chunk001.ts"} {
		if err := os.WriteFile(filepath.Join(segDir, seg), []byte("segment data"), 0644); err != nil {
			t.Fatal(err)
		}
	}

	req := &ipcpb.FreezeRequest{
		RequestId:    "req-dvr-seg",
		AssetHash:    "dvr-hash",
		AssetType:    "dvr",
		LocalPath:    dvrDir,
		InternalName: "stream-1",
		SegmentUrls: map[string]string{
			"dvr-hash.m3u8": "https://s3.example.com/dvr-hash.m3u8?presigned",
			"chunk000.ts":   "https://s3.example.com/chunk000.ts?presigned",
			"chunk001.ts":   "https://s3.example.com/chunk001.ts?presigned",
		},
	}

	sm.HandleFreezeRequest(req)

	if atomic.LoadInt64(&fake.uploadFileCalls) != 0 {
		t.Fatalf("expected no file uploads for whole-DVR freeze, got %d", atomic.LoadInt64(&fake.uploadFileCalls))
	}
	if atomic.LoadInt64(&fake.uploadCalls) != 0 {
		t.Fatalf("expected no manifest uploads for whole-DVR freeze, got %d", atomic.LoadInt64(&fake.uploadCalls))
	}
	if syncStatus != "failed" {
		t.Fatalf("expected sync status 'failed', got %q", syncStatus)
	}
}

func TestFreezeAsset_SkipUpload(t *testing.T) {
	sm := newTestStorageManager(t)
	fake := &fakePresignedClient{}
	sm.presignedClient = fake

	clipPath := filepath.Join(sm.basePath, "remote-clip.mp4")
	if err := os.WriteFile(clipPath, []byte("remote data"), 0644); err != nil {
		t.Fatal(err)
	}

	syncReported := false
	sm.requestFreezePermission = func(_ context.Context, _, _ string, _ uint64) (*ipcpb.FreezePermissionResponse, error) {
		return &ipcpb.FreezePermissionResponse{
			RequestId:  "req-skip",
			AssetHash:  "hash-remote",
			Approved:   true,
			SkipUpload: true,
		}, nil
	}
	sm.sendSyncComplete = func(_, _, _ string, _ uint64, _ string, _ bool, _ bool) error {
		syncReported = true
		return nil
	}

	asset := FreezeCandidate{
		AssetType: AssetTypeClip,
		AssetHash: "hash-remote",
		FilePath:  clipPath,
		SizeBytes: 11,
	}

	ctx := context.Background()
	err := sm.freezeAsset(ctx, asset)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if atomic.LoadInt64(&fake.uploadFileCalls) != 0 {
		t.Fatalf("expected zero uploads for skip_upload, got %d", atomic.LoadInt64(&fake.uploadFileCalls))
	}
	// FreezePermission only mints upload permission; it is not an eviction
	// authority. A remote skip_upload is a no-op for the freeze path — the
	// local warm copy is retained and dropped later by the CanDelete-gated
	// cleanup pass, not here.
	if syncReported {
		t.Fatal("freeze path must not report sync/eviction for a remote skip_upload")
	}
	if _, err := os.Stat(clipPath); err != nil {
		t.Fatalf("expected local file to be retained after skip_upload, stat err: %v", err)
	}
}

func TestFreezeAsset_PermissionDenied(t *testing.T) {
	sm := newTestStorageManager(t)

	sm.requestFreezePermission = func(_ context.Context, _, _ string, _ uint64) (*ipcpb.FreezePermissionResponse, error) {
		return &ipcpb.FreezePermissionResponse{
			Approved: false,
			Reason:   "quota exceeded",
		}, nil
	}

	clipPath := filepath.Join(sm.basePath, "denied.mp4")
	if err := os.WriteFile(clipPath, []byte("data"), 0644); err != nil {
		t.Fatal(err)
	}

	asset := FreezeCandidate{
		AssetType: AssetTypeClip,
		AssetHash: "hash-denied",
		FilePath:  clipPath,
		SizeBytes: 4,
	}

	ctx := context.Background()
	err := sm.freezeAsset(ctx, asset)
	if err == nil {
		t.Fatal("expected error for denied permission")
	}
	if got := err.Error(); got != "freeze not approved: quota exceeded" {
		t.Fatalf("unexpected error: %s", got)
	}
}

// TestUploadAsset_UploadFailureReportsSyncComplete covers the uploadAsset
// upload-failure branch: a mid-upload S3 error must report completion via
// sendSyncComplete with status "failed" (the shared failure path Foghorn maps
// to applySyncCompletionFailure), not silently swallow the error.
func TestUploadAsset_UploadFailureReportsSyncComplete(t *testing.T) {
	sm := newTestStorageManager(t)
	fake := &fakePresignedClient{uploadFileErr: fmt.Errorf("s3 upload boom")}
	sm.presignedClient = fake

	var (
		syncCalled bool
		syncStatus string
		syncErrMsg string
	)
	sm.sendSyncComplete = func(_, _, status string, _ uint64, errMsg string, _ bool, _ bool) error {
		syncCalled = true
		syncStatus = status
		syncErrMsg = errMsg
		return nil
	}

	clipPath := filepath.Join(sm.basePath, "clip.mp4")
	if err := os.WriteFile(clipPath, []byte("clip data"), 0644); err != nil {
		t.Fatal(err)
	}

	asset := FreezeCandidate{
		AssetType: AssetTypeClip,
		AssetHash: "hash-fail",
		FilePath:  clipPath,
		SizeBytes: 9,
	}
	permResp := &ipcpb.FreezePermissionResponse{
		RequestId:       "req-fail",
		AssetHash:       "hash-fail",
		Approved:        true,
		PresignedPutUrl: "https://s3.example.com/clip.mp4?presigned",
	}

	err := sm.uploadAsset(context.Background(), asset, permResp)
	if err == nil {
		t.Fatal("expected error when upload fails")
	}
	if !syncCalled {
		t.Fatal("expected failure path to report via sendSyncComplete")
	}
	if syncStatus != "failed" {
		t.Fatalf("expected sync status 'failed', got %q", syncStatus)
	}
	if syncErrMsg == "" {
		t.Fatal("expected non-empty error message in sync complete")
	}
}

// TestEvictClipVodCandidates_FreezingUnsyncedDoesNotCount proves that a
// candidate which is not yet durable on S3 is UPLOADED (frozen) for a later
// eviction pass but frees zero disk: freezeAsset retains the local file, so it
// must never count toward the byte target.
func TestEvictClipVodCandidates_FreezingUnsyncedDoesNotCount(t *testing.T) {
	sm := newTestStorageManager(t)
	fake := &fakePresignedClient{}
	sm.presignedClient = fake

	sm.requestCanDelete = func(_ context.Context, _ string) (bool, string, int64, error) {
		return false, "not_synced", 0, nil
	}
	sm.requestFreezePermission = func(_ context.Context, _, _ string, _ uint64) (*ipcpb.FreezePermissionResponse, error) {
		return &ipcpb.FreezePermissionResponse{Approved: true, PresignedPutUrl: "https://s3.example.com/put?presigned"}, nil
	}

	clipPath := filepath.Join(sm.basePath, "unsynced.mp4")
	payload := []byte("some clip bytes")
	if err := os.WriteFile(clipPath, payload, 0644); err != nil {
		t.Fatal(err)
	}
	size := uint64(len(payload))
	cand := FreezeCandidate{AssetType: AssetTypeClip, AssetHash: "hash-unsynced", FilePath: clipPath, SizeBytes: size}

	res := sm.evictClipVodCandidates([]FreezeCandidate{cand}, size)

	if res.freedBytes != 0 {
		t.Fatalf("freezing an unsynced candidate must free 0 bytes, got %d", res.freedBytes)
	}
	if res.deletedCount != 0 {
		t.Fatalf("expected 0 deletions, got %d", res.deletedCount)
	}
	if res.syncTriggered != 1 {
		t.Fatalf("expected 1 upload-for-later-eviction, got %d", res.syncTriggered)
	}
	if got := atomic.LoadInt64(&fake.uploadFileCalls); got != 1 {
		t.Fatalf("expected the unsynced asset to be uploaded once, got %d", got)
	}
	if _, err := os.Stat(clipPath); err != nil {
		t.Fatalf("freezing must RETAIN the local file, stat err: %v", err)
	}
}

// TestEvictClipVodCandidates_SyncedCandidateDeletedAndCounted proves that a
// CanDelete-approved candidate is deleted through the lease-guarded path, its
// sidecars are removed, and only its real bytes are counted as freed.
func TestEvictClipVodCandidates_SyncedCandidateDeletedAndCounted(t *testing.T) {
	sm := newTestStorageManager(t)
	sm.presignedClient = &fakePresignedClient{}

	sm.requestCanDelete = func(_ context.Context, _ string) (bool, string, int64, error) {
		return true, "remote_synced", 1234, nil
	}
	var deletedHash string
	sm.sendArtifactDeleted = func(hash, _, _, _ string, _ uint64) error {
		deletedHash = hash
		return nil
	}

	clipPath := filepath.Join(sm.basePath, "synced.mp4")
	payload := []byte("clip payload data")
	if err := os.WriteFile(clipPath, payload, 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(clipPath+".dtsh", []byte("dtsh"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(clipPath+".gop", []byte("gop"), 0644); err != nil {
		t.Fatal(err)
	}
	size := uint64(len(payload))
	cand := FreezeCandidate{AssetType: AssetTypeClip, AssetHash: "hash-synced", FilePath: clipPath, SizeBytes: size}

	res := sm.evictClipVodCandidates([]FreezeCandidate{cand}, size)

	if res.freedBytes != size {
		t.Fatalf("expected freedBytes=%d, got %d", size, res.freedBytes)
	}
	if res.deletedCount != 1 {
		t.Fatalf("expected 1 deletion, got %d", res.deletedCount)
	}
	if res.syncTriggered != 0 {
		t.Fatalf("expected 0 freeze uploads for an already-synced asset, got %d", res.syncTriggered)
	}
	if deletedHash != "hash-synced" {
		t.Fatalf("expected ArtifactDeleted for hash-synced, got %q", deletedHash)
	}
	if _, err := os.Stat(clipPath); !os.IsNotExist(err) {
		t.Fatalf("expected local file deleted, stat err: %v", err)
	}
	if _, err := os.Stat(clipPath + ".dtsh"); !os.IsNotExist(err) {
		t.Fatalf("expected .dtsh sidecar deleted, stat err: %v", err)
	}
	if _, err := os.Stat(clipPath + ".gop"); !os.IsNotExist(err) {
		t.Fatalf("expected .gop sidecar deleted, stat err: %v", err)
	}
}

// TestEvictClipVodCandidates_NoEarlyStopOnPhantomFreeze is the core regression:
// freezing uploads to S3 but RETAINS the local file, freeing zero disk. The old
// loop counted the frozen size toward the target and stopped early, leaving disk
// full while believing space was reclaimed. The fix must skip past the frozen
// (unsynced) candidate and reach the actually-deletable one.
func TestEvictClipVodCandidates_NoEarlyStopOnPhantomFreeze(t *testing.T) {
	sm := newTestStorageManager(t)
	sm.presignedClient = &fakePresignedClient{}
	sm.requestFreezePermission = func(_ context.Context, _, _ string, _ uint64) (*ipcpb.FreezePermissionResponse, error) {
		return &ipcpb.FreezePermissionResponse{Approved: true, PresignedPutUrl: "https://s3.example.com/put?presigned"}, nil
	}
	sm.requestCanDelete = func(_ context.Context, hash string) (bool, string, int64, error) {
		if hash == "hash-synced" {
			return true, "remote_synced", 0, nil
		}
		return false, "not_synced", 0, nil
	}

	unsyncedPath := filepath.Join(sm.basePath, "unsynced.mp4")
	syncedPath := filepath.Join(sm.basePath, "synced.mp4")
	payload := []byte("0123456789") // 10 bytes each
	if err := os.WriteFile(unsyncedPath, payload, 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(syncedPath, payload, 0644); err != nil {
		t.Fatal(err)
	}
	size := uint64(len(payload))

	// Unsynced candidate first so the old buggy loop would "free" the target by
	// freezing it and stop before ever reaching the deletable file.
	candidates := []FreezeCandidate{
		{AssetType: AssetTypeClip, AssetHash: "hash-unsynced", FilePath: unsyncedPath, SizeBytes: size},
		{AssetType: AssetTypeClip, AssetHash: "hash-synced", FilePath: syncedPath, SizeBytes: size},
	}

	res := sm.evictClipVodCandidates(candidates, size)

	if res.freedBytes != size {
		t.Fatalf("expected freedBytes=%d from the real delete, got %d", size, res.freedBytes)
	}
	if res.deletedCount != 1 || res.syncTriggered != 1 {
		t.Fatalf("expected 1 delete + 1 freeze, got deleted=%d synced=%d", res.deletedCount, res.syncTriggered)
	}
	if _, err := os.Stat(unsyncedPath); err != nil {
		t.Fatalf("frozen (unsynced) file must be retained, stat err: %v", err)
	}
	if _, err := os.Stat(syncedPath); !os.IsNotExist(err) {
		t.Fatalf("synced file must be deleted, stat err: %v", err)
	}
}

// writeReclaimFixture creates, under the manager's clips dir, a .blocks relay
// cache (blockBytes) and a full CanDelete-approved clip copy (copyBytes), and
// returns their paths. The clip is aged past minRetention so it enumerates as a
// freeze candidate.
func writeReclaimFixture(t *testing.T, sm *StorageManager, blockBytes, copyBytes int) (blocksDir, clipPath string) {
	t.Helper()
	clipsDir := filepath.Join(sm.basePath, "clips")
	blocksDir = filepath.Join(clipsDir, "relaycachehashaaaaaa.blocks")
	if err := os.MkdirAll(blocksDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(blocksDir, "block0.bin"), make([]byte, blockBytes), 0o644); err != nil {
		t.Fatal(err)
	}
	clipPath = filepath.Join(clipsDir, "fullcopycliphashaaaa.mp4")
	if err := os.WriteFile(clipPath, make([]byte, copyBytes), 0o644); err != nil {
		t.Fatal(err)
	}
	// Age the clip so it is not younger than minRetention (guards the boundary
	// where a just-written file's mtime equals "now").
	old := time.Now().Add(-time.Hour)
	if err := os.Chtimes(clipPath, old, old); err != nil {
		t.Fatal(err)
	}
	return blocksDir, clipPath
}

// TestReclaimToTarget_BlockCacheReclaimedBeforeFullCopy is the ordering
// regression: under normal pressure the engine must reclaim the cheap,
// rebuildable .blocks relay cache BEFORE a valuable full clip/VOD copy. When the
// block cache alone satisfies the target, the full copy — even though it is
// CanDelete-approved and present — must be left untouched.
func TestReclaimToTarget_BlockCacheReclaimedBeforeFullCopy(t *testing.T) {
	sm := newTestStorageManager(t)
	sm.presignedClient = &fakePresignedClient{}

	var canDeleteCalls int64
	// Full copy would be deletable if the loop ever reached it. It must not.
	sm.requestCanDelete = func(_ context.Context, _ string) (bool, string, int64, error) {
		atomic.AddInt64(&canDeleteCalls, 1)
		return true, "remote_synced", 0, nil
	}

	blocksDir, clipPath := writeReclaimFixture(t, sm, 100, 200)

	// Target is smaller than the block cache, so block eviction alone meets it.
	res := sm.reclaimToTarget(filepath.Join(sm.basePath, "clips"), 50)

	if res.freedBytes != 100 {
		t.Fatalf("expected 100 bytes freed from block cache, got %d", res.freedBytes)
	}
	if res.deletedCount != 0 {
		t.Fatalf("full copy must not be deleted when blocks satisfy the target, deletedCount=%d", res.deletedCount)
	}
	if got := atomic.LoadInt64(&canDeleteCalls); got != 0 {
		t.Fatalf("full-copy CanDelete must never be consulted when blocks suffice, calls=%d", got)
	}
	if _, err := os.Stat(blocksDir); !os.IsNotExist(err) {
		t.Fatalf("block cache must be evicted, stat err: %v", err)
	}
	if _, err := os.Stat(clipPath); err != nil {
		t.Fatalf("valuable full copy must be retained, stat err: %v", err)
	}
}

// TestReclaimToTarget_FullCopyReclaimedOnlyAfterBlocks proves the full copy is
// the LAST class evicted: when the block cache does not fully satisfy the
// target, the engine reclaims blocks first and only THEN deletes the
// CanDelete-approved full copy, counting only bytes actually deleted from disk
// (block bytes + copy bytes).
func TestReclaimToTarget_FullCopyReclaimedOnlyAfterBlocks(t *testing.T) {
	sm := newTestStorageManager(t)
	sm.presignedClient = &fakePresignedClient{}
	sm.requestCanDelete = func(_ context.Context, _ string) (bool, string, int64, error) {
		return true, "remote_synced", 0, nil
	}

	blocksDir, clipPath := writeReclaimFixture(t, sm, 100, 200)

	// Target exceeds the block cache, so the engine must fall through to the
	// full copy after evicting blocks.
	res := sm.reclaimToTarget(filepath.Join(sm.basePath, "clips"), 250)

	if res.freedBytes != 300 {
		t.Fatalf("expected 300 bytes freed (100 blocks + 200 copy), got %d", res.freedBytes)
	}
	if res.deletedCount != 1 {
		t.Fatalf("expected the full copy to be deleted after blocks, deletedCount=%d", res.deletedCount)
	}
	if _, err := os.Stat(blocksDir); !os.IsNotExist(err) {
		t.Fatalf("block cache must be evicted first, stat err: %v", err)
	}
	if _, err := os.Stat(clipPath); !os.IsNotExist(err) {
		t.Fatalf("full copy must be deleted after blocks were insufficient, stat err: %v", err)
	}
}

// TestReclaimToTarget_SingleActiveDVROvershootDoesNotUnderflow is the underflow regression: when the
// LAST (or only) active DVR frees MORE than the remaining deficit, the per-iteration guard cannot catch
// the overshoot, so the post-loop guard must return before the full-copy stage. Without it, the uint64
// (bytesToFree - res.freedBytes) wraps to an enormous target and the full-copy stage evicts every
// eligible copy. Here a single DVR overshoots the whole target, so the CanDelete-approved full copy must
// survive untouched.
func TestReclaimToTarget_SingleActiveDVROvershootDoesNotUnderflow(t *testing.T) {
	// No lease tracker is installed in these unit tests, so IsDestructiveCleanupAllowed() is true and the
	// DVR + full-copy stages run (same assumption as the sibling reclaim tests above).
	sm := newTestStorageManager(t)
	sm.presignedClient = &fakePresignedClient{}

	var canDeleteCalls int64
	sm.requestCanDelete = func(_ context.Context, _ string) (bool, string, int64, error) {
		atomic.AddInt64(&canDeleteCalls, 1)
		return true, "remote_synced", 0, nil
	}

	// One active DVR that frees 400 bytes — more than the 250 target — on its single eviction call.
	sm.activeDVRHashes = func() map[string]bool { return map[string]bool{"dvr-1": true} }
	var dvrCalls int64
	sm.evictDVRSegmentsFn = func(_ string, target uint64) (int, uint64) {
		atomic.AddInt64(&dvrCalls, 1)
		// The remaining deficit passed in must never be the underflowed near-max value.
		if target > 1<<40 {
			t.Fatalf("DVR stage received an underflowed target: %d", target)
		}
		return 3, 400
	}

	// A full copy exists and is CanDelete-approved; no block cache. If the target underflowed, the
	// full-copy stage would delete it.
	_, clipPath := writeReclaimFixture(t, sm, 0, 200)

	res := sm.reclaimToTarget(filepath.Join(sm.basePath, "clips"), 250)

	if atomic.LoadInt64(&dvrCalls) != 1 {
		t.Fatalf("expected exactly one DVR eviction call, got %d", atomic.LoadInt64(&dvrCalls))
	}
	if res.freedBytes != 400 {
		t.Fatalf("expected 400 bytes freed by the DVR alone, got %d", res.freedBytes)
	}
	if res.deletedCount != 0 {
		t.Fatalf("full copy must NOT be deleted once the DVR overshoots the target, deletedCount=%d", res.deletedCount)
	}
	if got := atomic.LoadInt64(&canDeleteCalls); got != 0 {
		t.Fatalf("full-copy CanDelete must never be consulted after the DVR satisfies the target, calls=%d", got)
	}
	if _, err := os.Stat(clipPath); err != nil {
		t.Fatalf("valuable full copy must be retained after DVR overshoot, stat err: %v", err)
	}
}

package handlers

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"

	"frameworks/api_sidecar/internal/storage"
	"frameworks/pkg/logging"
	pb "frameworks/pkg/proto"
)

type fakePresignedClient struct {
	uploadFileCalls  int64
	uploadCalls      int64
	downloadCalls    int64
	lastUploadedPath string
}

func (f *fakePresignedClient) UploadFileToPresignedURL(_ context.Context, _, localPath string, onProgress storage.ProgressCallback) error {
	atomic.AddInt64(&f.uploadFileCalls, 1)
	f.lastUploadedPath = localPath
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

		requestFreezePermission: func(_ context.Context, _, _, _ string, _ uint64, _ []string) (*pb.FreezePermissionResponse, error) {
			return nil, fmt.Errorf("not connected")
		},
		sendSyncComplete:     func(_, _, _, _ string, _ uint64, _ string, _ bool) error { return nil },
		sendFreezeComplete:   func(_, _, _, _ string, _ uint64, _ string) error { return nil },
		sendFreezeProgress:   func(_, _ string, _ uint32, _ uint64) error { return nil },
		sendStorageLifecycle: func(_ *pb.StorageLifecycleData) error { return nil },
		sendDefrostComplete:  func(_, _, _, _ string, _ uint64, _ string) error { return nil },
		sendDefrostProgress:  func(_, _ string, _ uint32, _ uint64, _, _ int32, _ string) error { return nil },
		requestCanDelete:     func(_ context.Context, _ string) (bool, string, int64, error) { return false, "", 0, nil },
		sendArtifactDeleted:  func(_, _, _, _ string, _ uint64) error { return nil },
	}
	sm.defrostTracker.inFlight = make(map[string]*DefrostJob)
	sm.freezeTracker.inFlight = make(map[string]bool)
	return sm
}

func TestHandleFreezeRequest_FileNotFound(t *testing.T) {
	sm := newTestStorageManager(t)

	req := &pb.FreezeRequest{
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

	req := &pb.FreezeRequest{
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

	permResp := &pb.FreezePermissionResponse{
		RequestId:       "req-1",
		AssetHash:       "hash-clip",
		Approved:        true,
		PresignedPutUrl: "", // No URL
		SegmentUrls:     nil,
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

	permResp := &pb.FreezePermissionResponse{
		RequestId:       "req-2",
		AssetHash:       "hash-vod",
		Approved:        true,
		PresignedPutUrl: "",
		SegmentUrls:     nil,
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

	permResp := &pb.FreezePermissionResponse{
		RequestId:   "req-3",
		AssetHash:   "hash-dvr",
		Approved:    true,
		SegmentUrls: nil, // No segment URLs for DVR
	}

	ctx := context.Background()
	err := sm.uploadAsset(ctx, asset, permResp)
	if err == nil {
		t.Fatal("expected error for missing DVR segment URLs")
	}
	if got := err.Error(); got != "no segment URLs provided for DVR freeze" {
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

	permResp := &pb.FreezePermissionResponse{
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

func TestUploadAsset_ClipSegmentURLsMissingMainFile(t *testing.T) {
	sm := newTestStorageManager(t)

	clipPath := filepath.Join(sm.basePath, "clip.mp4")
	if err := os.WriteFile(clipPath, []byte("fake"), 0644); err != nil {
		t.Fatal(err)
	}

	asset := FreezeCandidate{
		AssetType: AssetTypeClip,
		AssetHash: "hash-clip",
		FilePath:  clipPath,
		SizeBytes: 4,
	}

	// SegmentUrls provided but doesn't contain the main file name
	permResp := &pb.FreezePermissionResponse{
		RequestId:   "req-5",
		Approved:    true,
		SegmentUrls: map[string]string{"other.mp4": "https://example.com/presigned"},
	}

	ctx := context.Background()
	err := sm.uploadAsset(ctx, asset, permResp)
	if err == nil {
		t.Fatal("expected error when main file key missing from SegmentUrls")
	}
}

func TestFreezeTrackerCleansUpOnExit(t *testing.T) {
	sm := newTestStorageManager(t)

	asset := FreezeCandidate{
		AssetType: "unknown",
		AssetHash: "hash-track",
		SizeBytes: 100,
	}

	permResp := &pb.FreezePermissionResponse{
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

func TestHandleFreezeRequest_ClipUpload(t *testing.T) {
	sm := newTestStorageManager(t)
	fake := &fakePresignedClient{}
	sm.presignedClient = fake

	var syncStatus string
	sm.sendSyncComplete = func(_, _, status, _ string, _ uint64, _ string, _ bool) error {
		syncStatus = status
		return nil
	}

	clipPath := filepath.Join(sm.basePath, "clip.mp4")
	if err := os.WriteFile(clipPath, []byte("clip data here!"), 0644); err != nil {
		t.Fatal(err)
	}

	req := &pb.FreezeRequest{
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
	sm.sendSyncComplete = func(_, _, status, _ string, _ uint64, _ string, _ bool) error {
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

	req := &pb.FreezeRequest{
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

	// 2 segments + 1 initial manifest upload + 2 progress manifest uploads + 1 final manifest = 4 UploadToPresignedURL calls
	// 2 segment UploadFileToPresignedURL calls
	if atomic.LoadInt64(&fake.uploadFileCalls) != 2 {
		t.Fatalf("expected 2 file upload calls (segments), got %d", atomic.LoadInt64(&fake.uploadFileCalls))
	}
	if atomic.LoadInt64(&fake.uploadCalls) < 2 {
		t.Fatalf("expected at least 2 stream upload calls (manifests), got %d", atomic.LoadInt64(&fake.uploadCalls))
	}
	if syncStatus != "success" {
		t.Fatalf("expected sync status 'success', got %q", syncStatus)
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

	var syncStatus string
	sm.requestFreezePermission = func(_ context.Context, _, _, _ string, _ uint64, _ []string) (*pb.FreezePermissionResponse, error) {
		return &pb.FreezePermissionResponse{
			RequestId:  "req-skip",
			AssetHash:  "hash-remote",
			Approved:   true,
			SkipUpload: true,
		}, nil
	}
	sm.sendSyncComplete = func(_, _, status, _ string, _ uint64, _ string, _ bool) error {
		syncStatus = status
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
	if syncStatus != "evicted_remote" {
		t.Fatalf("expected status 'evicted_remote', got %q", syncStatus)
	}
	if _, err := os.Stat(clipPath); !os.IsNotExist(err) {
		t.Fatal("expected local file to be deleted after skip_upload")
	}
}

func TestFreezeAsset_PermissionDenied(t *testing.T) {
	sm := newTestStorageManager(t)

	sm.requestFreezePermission = func(_ context.Context, _, _, _ string, _ uint64, _ []string) (*pb.FreezePermissionResponse, error) {
		return &pb.FreezePermissionResponse{
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

func TestUploadAsset_ClipWithDtsh(t *testing.T) {
	sm := newTestStorageManager(t)
	fake := &fakePresignedClient{}
	sm.presignedClient = fake

	var dtshIncluded bool
	sm.sendSyncComplete = func(_, _, _, _ string, _ uint64, _ string, dtsh bool) error {
		dtshIncluded = dtsh
		return nil
	}

	clipPath := filepath.Join(sm.basePath, "clip.mp4")
	dtshPath := clipPath + ".dtsh"
	if err := os.WriteFile(clipPath, []byte("clip data"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dtshPath, []byte("dtsh data"), 0644); err != nil {
		t.Fatal(err)
	}

	asset := FreezeCandidate{
		AssetType: AssetTypeClip,
		AssetHash: "hash-dtsh",
		FilePath:  clipPath,
		SizeBytes: 9,
	}

	permResp := &pb.FreezePermissionResponse{
		RequestId: "req-dtsh",
		AssetHash: "hash-dtsh",
		Approved:  true,
		SegmentUrls: map[string]string{
			"clip.mp4":      "https://s3.example.com/clip.mp4?presigned",
			"clip.mp4.dtsh": "https://s3.example.com/clip.mp4.dtsh?presigned",
		},
	}

	ctx := context.Background()
	err := sm.uploadAsset(ctx, asset, permResp)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// 1 for clip.mp4 + 1 for clip.mp4.dtsh
	if atomic.LoadInt64(&fake.uploadFileCalls) != 2 {
		t.Fatalf("expected 2 upload file calls (clip + dtsh), got %d", atomic.LoadInt64(&fake.uploadFileCalls))
	}
	if !dtshIncluded {
		t.Fatal("expected dtshIncluded=true in sync complete")
	}
}

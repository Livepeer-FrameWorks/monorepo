package handlers

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"frameworks/api_sidecar/internal/storage"
	"frameworks/pkg/logging"
	pb "frameworks/pkg/proto"
)

// configurablePresignedClient extends fakePresignedClient with configurable
// download behavior for defrost tests.
type configurablePresignedClient struct {
	downloadFileCalls int64
	downloadCalls     int64
	uploadFileCalls   int64

	downloadFileErr  error                  // If set, DownloadToFileFromPresignedURL returns this
	downloadErr      error                  // If set, DownloadFromPresignedURL returns this
	downloadContent  []byte                 // Content written to writer in DownloadFromPresignedURL
	downloadFileHook func(url, path string) // Called after successful download (to create the file)
}

func (c *configurablePresignedClient) UploadFileToPresignedURL(_ context.Context, _, _ string, _ storage.ProgressCallback) error {
	atomic.AddInt64(&c.uploadFileCalls, 1)
	return nil
}

func (c *configurablePresignedClient) UploadToPresignedURL(_ context.Context, _ string, _ io.Reader, _ int64, _ storage.ProgressCallback) error {
	return nil
}

func (c *configurablePresignedClient) DownloadToFileFromPresignedURL(_ context.Context, url, localPath string, cb storage.ProgressCallback) error {
	atomic.AddInt64(&c.downloadFileCalls, 1)
	if c.downloadFileErr != nil {
		return c.downloadFileErr
	}
	// Create the file so os.Stat works after download
	if err := os.MkdirAll(filepath.Dir(localPath), 0755); err != nil {
		return err
	}
	if err := os.WriteFile(localPath, make([]byte, 4096), 0644); err != nil {
		return err
	}
	if c.downloadFileHook != nil {
		c.downloadFileHook(url, localPath)
	}
	if cb != nil {
		cb(4096)
	}
	return nil
}

func (c *configurablePresignedClient) DownloadFromPresignedURL(_ context.Context, _ string, w io.Writer, _ storage.ProgressCallback) (int64, error) {
	atomic.AddInt64(&c.downloadCalls, 1)
	if c.downloadErr != nil {
		return 0, c.downloadErr
	}
	content := c.downloadContent
	if content == nil {
		content = []byte{}
	}
	n, err := w.Write(content)
	return int64(n), err
}

func newDefrostTestSM(t *testing.T, client *configurablePresignedClient) *StorageManager {
	t.Helper()
	sm := &StorageManager{
		logger:          logging.NewLogger(),
		basePath:        t.TempDir(),
		presignedClient: client,

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

// --- DefrostSingleFile (via DefrostClip/DefrostVOD) ---

func TestDefrostClip_AlreadyLocal(t *testing.T) {
	client := &configurablePresignedClient{}
	sm := newDefrostTestSM(t, client)

	// Create the file so defrost sees it as already local
	clipDir := filepath.Join(sm.basePath, "clips", "stream-1")
	if err := os.MkdirAll(clipDir, 0755); err != nil {
		t.Fatal(err)
	}
	localPath := filepath.Join(clipDir, "hash-local.mp4")
	if err := os.WriteFile(localPath, make([]byte, 2048), 0644); err != nil {
		t.Fatal(err)
	}

	var completedHash string
	var completedStatus string
	sm.sendDefrostComplete = func(_, assetHash, status, _ string, _ uint64, _ string) error {
		completedHash = assetHash
		completedStatus = status
		return nil
	}

	var lifecycleAction pb.StorageLifecycleData_Action
	sm.sendStorageLifecycle = func(data *pb.StorageLifecycleData) error {
		lifecycleAction = data.Action
		return nil
	}

	req := &pb.DefrostRequest{
		RequestId: "req-1",
		AssetHash: "hash-local",
		AssetType: "clip",
		LocalPath: localPath,
	}

	result, err := sm.DefrostClip(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Status != "success" {
		t.Fatalf("expected success, got %s", result.Status)
	}
	if result.SizeBytes != 2048 {
		t.Fatalf("expected 2048 bytes, got %d", result.SizeBytes)
	}
	if completedHash != "hash-local" {
		t.Fatalf("expected sendDefrostComplete called with hash-local, got %s", completedHash)
	}
	if completedStatus != "success" {
		t.Fatalf("expected success status, got %s", completedStatus)
	}
	if lifecycleAction != pb.StorageLifecycleData_ACTION_CACHED {
		t.Fatalf("expected ACTION_CACHED lifecycle, got %v", lifecycleAction)
	}
	// No download should have occurred
	if atomic.LoadInt64(&client.downloadFileCalls) != 0 {
		t.Fatal("expected no download calls for already-local file")
	}
}

func TestDefrostClip_NoPresignedURL(t *testing.T) {
	client := &configurablePresignedClient{}
	sm := newDefrostTestSM(t, client)

	var completedStatus string
	sm.sendDefrostComplete = func(_, _, status, _ string, _ uint64, _ string) error {
		completedStatus = status
		return nil
	}

	req := &pb.DefrostRequest{
		RequestId:       "req-no-url",
		AssetHash:       "hash-nourl",
		AssetType:       "clip",
		LocalPath:       filepath.Join(sm.basePath, "clips", "hash-nourl.mp4"),
		PresignedGetUrl: "", // empty
	}

	_, err := sm.DefrostClip(context.Background(), req)
	if err == nil {
		t.Fatal("expected error for missing presigned URL")
	}
	if !strings.Contains(err.Error(), "no presigned GET URL") {
		t.Fatalf("expected 'no presigned GET URL' error, got: %s", err.Error())
	}
	if completedStatus != "failed" {
		t.Fatalf("expected failed status, got %s", completedStatus)
	}
}

func TestDefrostClip_DownloadSuccess(t *testing.T) {
	client := &configurablePresignedClient{}
	sm := newDefrostTestSM(t, client)

	var completedStatus string
	var completedSizeBytes uint64
	sm.sendDefrostComplete = func(_, _, status, _ string, sizeBytes uint64, _ string) error {
		completedStatus = status
		completedSizeBytes = sizeBytes
		return nil
	}

	var progressSent bool
	sm.sendDefrostProgress = func(_, _ string, _ uint32, _ uint64, _, _ int32, _ string) error {
		progressSent = true
		return nil
	}

	var lifecycleActions []pb.StorageLifecycleData_Action
	sm.sendStorageLifecycle = func(data *pb.StorageLifecycleData) error {
		lifecycleActions = append(lifecycleActions, data.Action)
		return nil
	}

	localPath := filepath.Join(sm.basePath, "clips", "stream-1", "hash-dl.mp4")
	req := &pb.DefrostRequest{
		RequestId:       "req-dl",
		AssetHash:       "hash-dl",
		AssetType:       "clip",
		LocalPath:       localPath,
		PresignedGetUrl: "https://s3.example.com/presigned/clip.mp4",
	}

	result, err := sm.DefrostClip(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Status != "success" {
		t.Fatalf("expected success, got %s", result.Status)
	}
	if completedStatus != "success" {
		t.Fatalf("expected sendDefrostComplete with success, got %s", completedStatus)
	}
	if completedSizeBytes == 0 {
		t.Fatal("expected non-zero size bytes in completion")
	}
	if progressSent != true {
		t.Fatal("expected progress to be sent during download")
	}
	// Lifecycle: CACHE_STARTED then CACHED
	if len(lifecycleActions) < 2 {
		t.Fatalf("expected at least 2 lifecycle events, got %d", len(lifecycleActions))
	}
	if lifecycleActions[0] != pb.StorageLifecycleData_ACTION_CACHE_STARTED {
		t.Fatalf("expected first lifecycle = CACHE_STARTED, got %v", lifecycleActions[0])
	}
	if lifecycleActions[len(lifecycleActions)-1] != pb.StorageLifecycleData_ACTION_CACHED {
		t.Fatalf("expected last lifecycle = CACHED, got %v", lifecycleActions[len(lifecycleActions)-1])
	}
	if atomic.LoadInt64(&client.downloadFileCalls) != 1 {
		t.Fatalf("expected 1 download call, got %d", atomic.LoadInt64(&client.downloadFileCalls))
	}
}

func TestDefrostClip_DownloadError(t *testing.T) {
	client := &configurablePresignedClient{
		downloadFileErr: fmt.Errorf("network timeout"),
	}
	sm := newDefrostTestSM(t, client)

	var completedStatus string
	var completedErrMsg string
	sm.sendDefrostComplete = func(_, _, status, _ string, _ uint64, errMsg string) error {
		completedStatus = status
		completedErrMsg = errMsg
		return nil
	}

	localPath := filepath.Join(sm.basePath, "clips", "stream-1", "hash-fail.mp4")
	req := &pb.DefrostRequest{
		RequestId:       "req-fail",
		AssetHash:       "hash-fail",
		AssetType:       "clip",
		LocalPath:       localPath,
		PresignedGetUrl: "https://s3.example.com/presigned/clip.mp4",
	}

	_, err := sm.DefrostClip(context.Background(), req)
	if err == nil {
		t.Fatal("expected error for download failure")
	}
	if completedStatus != "failed" {
		t.Fatalf("expected failed status, got %s", completedStatus)
	}
	if !strings.Contains(completedErrMsg, "network timeout") {
		t.Fatalf("expected error message to contain 'network timeout', got: %s", completedErrMsg)
	}
}

func TestDefrostClip_WithDtsh(t *testing.T) {
	var downloadedPaths []string
	client := &configurablePresignedClient{
		downloadFileHook: func(_, path string) {
			downloadedPaths = append(downloadedPaths, path)
		},
	}
	sm := newDefrostTestSM(t, client)

	localPath := filepath.Join(sm.basePath, "clips", "stream-1", "hash-dtsh.mp4")
	req := &pb.DefrostRequest{
		RequestId:       "req-dtsh",
		AssetHash:       "hash-dtsh",
		AssetType:       "clip",
		LocalPath:       localPath,
		PresignedGetUrl: "https://s3.example.com/presigned/clip.mp4",
		SegmentUrls: map[string]string{
			"hash-dtsh.mp4.dtsh": "https://s3.example.com/presigned/clip.mp4.dtsh",
		},
	}

	result, err := sm.DefrostClip(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Status != "success" {
		t.Fatalf("expected success, got %s", result.Status)
	}
	// Should have downloaded both main file and dtsh
	if atomic.LoadInt64(&client.downloadFileCalls) != 2 {
		t.Fatalf("expected 2 download calls (main + dtsh), got %d", atomic.LoadInt64(&client.downloadFileCalls))
	}
}

func TestDefrostVOD_HappyPath(t *testing.T) {
	client := &configurablePresignedClient{}
	sm := newDefrostTestSM(t, client)

	var completedStatus string
	sm.sendDefrostComplete = func(_, _, status, _ string, _ uint64, _ string) error {
		completedStatus = status
		return nil
	}

	localPath := filepath.Join(sm.basePath, "vod", "hash-vod.mp4")
	req := &pb.DefrostRequest{
		RequestId:       "req-vod",
		AssetHash:       "hash-vod",
		AssetType:       "vod",
		LocalPath:       localPath,
		PresignedGetUrl: "https://s3.example.com/presigned/vod.mp4",
	}

	result, err := sm.DefrostVOD(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Status != "success" {
		t.Fatalf("expected success, got %s", result.Status)
	}
	if completedStatus != "success" {
		t.Fatal("expected sendDefrostComplete called with success")
	}
}

// --- DefrostDVR ---

func TestDefrostDVR_AlreadyLocal(t *testing.T) {
	client := &configurablePresignedClient{}
	sm := newDefrostTestSM(t, client)

	// Create DVR directory with manifest
	dvrDir := filepath.Join(sm.basePath, "dvr", "stream-1", "hash-dvr-local")
	if err := os.MkdirAll(dvrDir, 0755); err != nil {
		t.Fatal(err)
	}
	manifest := "#EXTM3U\n#EXT-X-TARGETDURATION:6\n#EXTINF:6.000,\nsegments/chunk000.ts\n#EXT-X-ENDLIST\n"
	manifestPath := filepath.Join(dvrDir, "hash-dvr-local.m3u8")
	if err := os.WriteFile(manifestPath, []byte(manifest), 0644); err != nil {
		t.Fatal(err)
	}
	segDir := filepath.Join(dvrDir, "segments")
	if err := os.MkdirAll(segDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(segDir, "chunk000.ts"), make([]byte, 5000), 0644); err != nil {
		t.Fatal(err)
	}

	var completedStatus string
	sm.sendDefrostComplete = func(_, _, status, _ string, _ uint64, _ string) error {
		completedStatus = status
		return nil
	}

	req := &pb.DefrostRequest{
		RequestId: "req-dvr-local",
		AssetHash: "hash-dvr-local",
		AssetType: "dvr",
		LocalPath: dvrDir,
	}

	result, err := sm.DefrostDVR(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Status != "success" {
		t.Fatalf("expected success, got %s", result.Status)
	}
	if completedStatus != "success" {
		t.Fatalf("expected sendDefrostComplete with success, got %s", completedStatus)
	}
	// Should report total bytes from walk
	if result.SizeBytes == 0 {
		t.Fatal("expected non-zero size from walk")
	}
	// No download
	if atomic.LoadInt64(&client.downloadFileCalls) != 0 {
		t.Fatal("expected no download for already-local DVR")
	}
}

func TestDefrostDVR_NoSegmentURLs(t *testing.T) {
	client := &configurablePresignedClient{}
	sm := newDefrostTestSM(t, client)

	var completedStatus string
	sm.sendDefrostComplete = func(_, _, status, _ string, _ uint64, _ string) error {
		completedStatus = status
		return nil
	}

	req := &pb.DefrostRequest{
		RequestId:   "req-dvr-noseg",
		AssetHash:   "hash-dvr-noseg",
		AssetType:   "dvr",
		LocalPath:   filepath.Join(sm.basePath, "dvr", "stream-1", "hash-dvr-noseg"),
		SegmentUrls: map[string]string{}, // empty
	}

	_, err := sm.DefrostDVR(context.Background(), req)
	if err == nil {
		t.Fatal("expected error for empty segment URLs")
	}
	if !strings.Contains(err.Error(), "no segment URLs") {
		t.Fatalf("expected 'no segment URLs' error, got: %s", err.Error())
	}
	if completedStatus != "failed" {
		t.Fatalf("expected failed status, got %s", completedStatus)
	}
}

func TestDefrostDVR_NoManifestURL(t *testing.T) {
	client := &configurablePresignedClient{}
	sm := newDefrostTestSM(t, client)

	var completedStatus string
	sm.sendDefrostComplete = func(_, _, status, _ string, _ uint64, _ string) error {
		completedStatus = status
		return nil
	}

	req := &pb.DefrostRequest{
		RequestId: "req-dvr-nomanifest",
		AssetHash: "hash-dvr-nomanifest",
		AssetType: "dvr",
		LocalPath: filepath.Join(sm.basePath, "dvr", "stream-1", "hash-dvr-nomanifest"),
		SegmentUrls: map[string]string{
			"chunk000.ts": "https://s3.example.com/seg0",
			// No manifest key (hash.m3u8)
		},
	}

	_, err := sm.DefrostDVR(context.Background(), req)
	if err == nil {
		t.Fatal("expected error for missing manifest URL")
	}
	if !strings.Contains(err.Error(), "no manifest URL") {
		t.Fatalf("expected 'no manifest URL' error, got: %s", err.Error())
	}
	if completedStatus != "failed" {
		t.Fatalf("expected failed status, got %s", completedStatus)
	}
}

func TestDefrostDVR_FullDownload(t *testing.T) {
	manifestContent := "#EXTM3U\n#EXT-X-VERSION:3\n#EXT-X-TARGETDURATION:6\n#EXTINF:6.000,\nchunk000.ts\n#EXTINF:5.500,\nchunk001.ts\n#EXT-X-ENDLIST\n"

	client := &configurablePresignedClient{
		downloadContent: []byte(manifestContent),
	}
	sm := newDefrostTestSM(t, client)

	var completedStatus string
	sm.sendDefrostComplete = func(_, _, status, _ string, _ uint64, _ string) error {
		completedStatus = status
		return nil
	}

	var mu sync.Mutex
	var progressMessages []string
	sm.sendDefrostProgress = func(_, _ string, _ uint32, _ uint64, _, _ int32, msg string) error {
		mu.Lock()
		progressMessages = append(progressMessages, msg)
		mu.Unlock()
		return nil
	}

	var lifecycleActions []pb.StorageLifecycleData_Action
	sm.sendStorageLifecycle = func(data *pb.StorageLifecycleData) error {
		lifecycleActions = append(lifecycleActions, data.Action)
		return nil
	}

	dvrDir := filepath.Join(sm.basePath, "dvr", "stream-1", "hash-dvr-full")
	req := &pb.DefrostRequest{
		RequestId: "req-dvr-full",
		AssetHash: "hash-dvr-full",
		AssetType: "dvr",
		LocalPath: dvrDir,
		SegmentUrls: map[string]string{
			"hash-dvr-full.m3u8": "https://s3.example.com/manifest",
			"chunk000.ts":        "https://s3.example.com/seg0",
			"chunk001.ts":        "https://s3.example.com/seg1",
		},
	}

	result, err := sm.DefrostDVR(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Status != "success" {
		t.Fatalf("expected success, got %s", result.Status)
	}
	if completedStatus != "success" {
		t.Fatalf("expected sendDefrostComplete with success, got %s", completedStatus)
	}
	// Downloaded: manifest (DownloadFromPresignedURL) + 2 segments (DownloadToFileFromPresignedURL)
	if atomic.LoadInt64(&client.downloadCalls) != 1 {
		t.Fatalf("expected 1 manifest download call, got %d", atomic.LoadInt64(&client.downloadCalls))
	}
	if atomic.LoadInt64(&client.downloadFileCalls) != 2 {
		t.Fatalf("expected 2 segment download calls, got %d", atomic.LoadInt64(&client.downloadFileCalls))
	}
	// Progress: "ready" then 2x "downloading"
	if len(progressMessages) < 2 {
		t.Fatalf("expected at least 2 progress messages, got %d: %v", len(progressMessages), progressMessages)
	}
	if progressMessages[0] != "ready" {
		t.Fatalf("expected first progress = 'ready', got %s", progressMessages[0])
	}
	// Lifecycle: CACHE_STARTED then CACHED
	hasStarted := false
	hasCached := false
	for _, a := range lifecycleActions {
		if a == pb.StorageLifecycleData_ACTION_CACHE_STARTED {
			hasStarted = true
		}
		if a == pb.StorageLifecycleData_ACTION_CACHED {
			hasCached = true
		}
	}
	if !hasStarted {
		t.Fatal("expected CACHE_STARTED lifecycle event")
	}
	if !hasCached {
		t.Fatal("expected CACHED lifecycle event")
	}
	// Manifest should have been finalized with #EXT-X-ENDLIST
	manifestPath := filepath.Join(dvrDir, "hash-dvr-full.m3u8")
	content, err := os.ReadFile(manifestPath)
	if err != nil {
		t.Fatalf("failed to read manifest: %v", err)
	}
	if !strings.Contains(string(content), "#EXT-X-ENDLIST") {
		t.Fatal("expected manifest to contain #EXT-X-ENDLIST after finalization")
	}
}

func TestDefrostDVR_SegmentDownloadError(t *testing.T) {
	manifestContent := "#EXTM3U\n#EXT-X-VERSION:3\n#EXT-X-TARGETDURATION:6\n#EXTINF:6.000,\nchunk000.ts\n#EXT-X-ENDLIST\n"

	client := &configurablePresignedClient{
		downloadContent: []byte(manifestContent),
		downloadFileErr: fmt.Errorf("segment download failed"),
	}
	sm := newDefrostTestSM(t, client)

	var completedStatus string
	sm.sendDefrostComplete = func(_, _, status, _ string, _ uint64, _ string) error {
		completedStatus = status
		return nil
	}

	dvrDir := filepath.Join(sm.basePath, "dvr", "stream-1", "hash-dvr-segfail")
	req := &pb.DefrostRequest{
		RequestId: "req-dvr-segfail",
		AssetHash: "hash-dvr-segfail",
		AssetType: "dvr",
		LocalPath: dvrDir,
		SegmentUrls: map[string]string{
			"hash-dvr-segfail.m3u8": "https://s3.example.com/manifest",
			"chunk000.ts":           "https://s3.example.com/seg0",
		},
	}

	_, err := sm.DefrostDVR(context.Background(), req)
	if err == nil {
		t.Fatal("expected error for segment download failure")
	}
	if !strings.Contains(err.Error(), "segment download failed") {
		t.Fatalf("expected segment download error, got: %s", err.Error())
	}
	if completedStatus != "failed" {
		t.Fatalf("expected failed status, got %s", completedStatus)
	}
}

func TestDefrostDVR_ManifestDownloadError(t *testing.T) {
	client := &configurablePresignedClient{
		downloadErr: fmt.Errorf("manifest download failed"),
	}
	sm := newDefrostTestSM(t, client)

	var completedStatus string
	sm.sendDefrostComplete = func(_, _, status, _ string, _ uint64, _ string) error {
		completedStatus = status
		return nil
	}

	dvrDir := filepath.Join(sm.basePath, "dvr", "stream-1", "hash-dvr-manfail")
	req := &pb.DefrostRequest{
		RequestId: "req-dvr-manfail",
		AssetHash: "hash-dvr-manfail",
		AssetType: "dvr",
		LocalPath: dvrDir,
		SegmentUrls: map[string]string{
			"hash-dvr-manfail.m3u8": "https://s3.example.com/manifest",
			"chunk000.ts":           "https://s3.example.com/seg0",
		},
	}

	_, err := sm.DefrostDVR(context.Background(), req)
	if err == nil {
		t.Fatal("expected error for manifest download failure")
	}
	if completedStatus != "failed" {
		t.Fatalf("expected failed status, got %s", completedStatus)
	}
}

// --- Defrost Job Deduplication ---

func TestDefrostClip_Deduplication(t *testing.T) {
	client := &configurablePresignedClient{}
	sm := newDefrostTestSM(t, client)

	localPath := filepath.Join(sm.basePath, "clips", "stream-1", "hash-dedup.mp4")
	req := &pb.DefrostRequest{
		RequestId:       "req-dedup",
		AssetHash:       "hash-dedup",
		AssetType:       "clip",
		LocalPath:       localPath,
		PresignedGetUrl: "https://s3.example.com/presigned/clip.mp4",
	}

	// First defrost in background
	var wg sync.WaitGroup
	wg.Add(2)
	var result1, result2 *pb.DefrostComplete
	var err1, err2 error

	go func() {
		defer wg.Done()
		result1, err1 = sm.DefrostClip(context.Background(), req)
	}()

	go func() {
		defer wg.Done()
		result2, err2 = sm.DefrostClip(context.Background(), &pb.DefrostRequest{
			RequestId:       "req-dedup-2",
			AssetHash:       "hash-dedup",
			AssetType:       "clip",
			LocalPath:       localPath,
			PresignedGetUrl: "https://s3.example.com/presigned/clip.mp4",
		})
	}()

	wg.Wait()

	if err1 != nil {
		t.Fatalf("first defrost error: %v", err1)
	}
	if err2 != nil {
		t.Fatalf("second defrost error: %v", err2)
	}
	if result1.Status != "success" || result2.Status != "success" {
		t.Fatalf("expected both success, got %s and %s", result1.Status, result2.Status)
	}
}

// --- FallbackCleanup ---

func TestFallbackCleanup_SafeToDelete(t *testing.T) {
	client := &configurablePresignedClient{}
	sm := newDefrostTestSM(t, client)
	sm.targetThreshold = 0.70
	sm.minRetentionHours = 0 // Allow all files as candidates

	// Create clip files
	clipsDir := filepath.Join(sm.basePath, "clips")
	streamDir := filepath.Join(clipsDir, "stream-1")
	if err := os.MkdirAll(streamDir, 0755); err != nil {
		t.Fatal(err)
	}
	clipPath := filepath.Join(streamDir, "aabbccddeeff001122.mp4")
	if err := os.WriteFile(clipPath, make([]byte, 8192), 0644); err != nil {
		t.Fatal(err)
	}

	sm.requestCanDelete = func(_ context.Context, _ string) (bool, string, int64, error) {
		return true, "synced to S3", int64(60000), nil
	}

	var deletedHash string
	var deletedReason string
	sm.sendArtifactDeleted = func(hash, _, reason, _ string, _ uint64) error {
		deletedHash = hash
		deletedReason = reason
		return nil
	}

	var evictionSent bool
	sm.sendStorageLifecycle = func(data *pb.StorageLifecycleData) error {
		if data.Action == pb.StorageLifecycleData_ACTION_EVICTED {
			evictionSent = true
		}
		return nil
	}

	// usedBytes > targetBytes to trigger cleanup
	totalBytes := uint64(100000)
	usedBytes := uint64(90000) // 90% used, target 70% → need to free 20000

	err := sm.fallbackCleanup(clipsDir, usedBytes, totalBytes)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// File should be deleted
	if _, err := os.Stat(clipPath); !os.IsNotExist(err) {
		t.Fatal("expected clip file to be deleted")
	}
	if deletedHash != "aabbccddeeff001122" {
		t.Fatal("expected sendArtifactDeleted to be called")
	}
	if deletedReason != "eviction" {
		t.Fatalf("expected reason 'eviction', got %s", deletedReason)
	}
	if !evictionSent {
		t.Fatal("expected ACTION_EVICTED lifecycle event")
	}
}

func TestFallbackCleanup_NotSafeToDelete(t *testing.T) {
	client := &configurablePresignedClient{}
	sm := newDefrostTestSM(t, client)
	sm.targetThreshold = 0.70
	sm.minRetentionHours = 0

	clipsDir := filepath.Join(sm.basePath, "clips")
	streamDir := filepath.Join(clipsDir, "stream-1")
	if err := os.MkdirAll(streamDir, 0755); err != nil {
		t.Fatal(err)
	}
	clipPath := filepath.Join(streamDir, "aabbccddeeff001122.mp4")
	if err := os.WriteFile(clipPath, make([]byte, 8192), 0644); err != nil {
		t.Fatal(err)
	}

	sm.requestCanDelete = func(_ context.Context, _ string) (bool, string, int64, error) {
		return false, "not synced", 0, nil
	}

	var artifactDeleteCalled bool
	sm.sendArtifactDeleted = func(_, _, _, _ string, _ uint64) error {
		artifactDeleteCalled = true
		return nil
	}

	totalBytes := uint64(100000)
	usedBytes := uint64(90000)

	err := sm.fallbackCleanup(clipsDir, usedBytes, totalBytes)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// File should NOT be deleted (not synced)
	if _, err := os.Stat(clipPath); os.IsNotExist(err) {
		t.Fatal("expected clip file to be preserved (not synced)")
	}
	if artifactDeleteCalled {
		t.Fatal("expected sendArtifactDeleted NOT to be called")
	}
}

func TestFallbackCleanup_RequestCanDeleteError(t *testing.T) {
	client := &configurablePresignedClient{}
	sm := newDefrostTestSM(t, client)
	sm.targetThreshold = 0.70
	sm.minRetentionHours = 0

	clipsDir := filepath.Join(sm.basePath, "clips")
	streamDir := filepath.Join(clipsDir, "stream-1")
	if err := os.MkdirAll(streamDir, 0755); err != nil {
		t.Fatal(err)
	}
	clipPath := filepath.Join(streamDir, "aabbccddeeff001122.mp4")
	if err := os.WriteFile(clipPath, make([]byte, 8192), 0644); err != nil {
		t.Fatal(err)
	}

	sm.requestCanDelete = func(_ context.Context, _ string) (bool, string, int64, error) {
		return false, "", 0, fmt.Errorf("foghorn unreachable")
	}

	totalBytes := uint64(100000)
	usedBytes := uint64(90000)

	err := sm.fallbackCleanup(clipsDir, usedBytes, totalBytes)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// File should NOT be deleted (data safety when Foghorn unreachable)
	if _, err := os.Stat(clipPath); os.IsNotExist(err) {
		t.Fatal("expected clip file to be preserved when Foghorn is unreachable")
	}
}

func TestFallbackCleanup_DeleteAuxiliaryFiles(t *testing.T) {
	client := &configurablePresignedClient{}
	sm := newDefrostTestSM(t, client)
	sm.targetThreshold = 0.70
	sm.minRetentionHours = 0

	clipsDir := filepath.Join(sm.basePath, "clips")
	streamDir := filepath.Join(clipsDir, "stream-1")
	if err := os.MkdirAll(streamDir, 0755); err != nil {
		t.Fatal(err)
	}
	clipPath := filepath.Join(streamDir, "aabbccddeeff001122.mp4")
	dtshPath := clipPath + ".dtsh"
	gopPath := clipPath + ".gop"
	if err := os.WriteFile(clipPath, make([]byte, 8192), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dtshPath, make([]byte, 256), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(gopPath, make([]byte, 128), 0644); err != nil {
		t.Fatal(err)
	}

	sm.requestCanDelete = func(_ context.Context, _ string) (bool, string, int64, error) {
		return true, "synced", int64(30000), nil
	}

	sm.sendArtifactDeleted = func(_, _, _, _ string, _ uint64) error { return nil }

	totalBytes := uint64(100000)
	usedBytes := uint64(90000)

	err := sm.fallbackCleanup(clipsDir, usedBytes, totalBytes)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Main file + auxiliary files should all be deleted
	if _, err := os.Stat(clipPath); !os.IsNotExist(err) {
		t.Fatal("expected .mp4 to be deleted")
	}
	if _, err := os.Stat(dtshPath); !os.IsNotExist(err) {
		t.Fatal("expected .dtsh to be deleted")
	}
	if _, err := os.Stat(gopPath); !os.IsNotExist(err) {
		t.Fatal("expected .gop to be deleted")
	}
}

func TestFallbackCleanup_NoCandidates(t *testing.T) {
	client := &configurablePresignedClient{}
	sm := newDefrostTestSM(t, client)
	sm.targetThreshold = 0.70

	clipsDir := filepath.Join(sm.basePath, "clips")
	if err := os.MkdirAll(clipsDir, 0755); err != nil {
		t.Fatal(err)
	}

	totalBytes := uint64(100000)
	usedBytes := uint64(90000)

	// No files to delete — should not error
	err := sm.fallbackCleanup(clipsDir, usedBytes, totalBytes)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestFallbackCleanup_StopsAfterFreeing(t *testing.T) {
	client := &configurablePresignedClient{}
	sm := newDefrostTestSM(t, client)
	sm.targetThreshold = 0.70
	sm.minRetentionHours = 0

	clipsDir := filepath.Join(sm.basePath, "clips")
	streamDir := filepath.Join(clipsDir, "stream-1")
	if err := os.MkdirAll(streamDir, 0755); err != nil {
		t.Fatal(err)
	}

	// Create 3 files, each 10000 bytes
	for _, name := range []string{"aabbccddeeff000001.mp4", "aabbccddeeff000002.mp4", "aabbccddeeff000003.mp4"} {
		if err := os.WriteFile(filepath.Join(streamDir, name), make([]byte, 10000), 0644); err != nil {
			t.Fatal(err)
		}
	}

	sm.requestCanDelete = func(_ context.Context, _ string) (bool, string, int64, error) {
		return true, "synced", int64(5000), nil
	}

	var deleteCount int64
	sm.sendArtifactDeleted = func(_, _, _, _ string, _ uint64) error {
		atomic.AddInt64(&deleteCount, 1)
		return nil
	}

	// totalBytes=100000, usedBytes=75000, target=70% → need to free 5000
	// First file (10000 bytes) should be enough
	totalBytes := uint64(100000)
	usedBytes := uint64(75000)

	err := sm.fallbackCleanup(clipsDir, usedBytes, totalBytes)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Should delete only 1 file (10000 > 5000 needed)
	if atomic.LoadInt64(&deleteCount) != 1 {
		t.Fatalf("expected 1 deletion, got %d", atomic.LoadInt64(&deleteCount))
	}
}

func TestFallbackCleanup_DVRDirectory(t *testing.T) {
	client := &configurablePresignedClient{}
	sm := newDefrostTestSM(t, client)
	sm.targetThreshold = 0.70
	sm.minRetentionHours = 0

	// Create DVR directory structure
	dvrDir := filepath.Join(sm.basePath, "dvr")
	recordingDir := filepath.Join(dvrDir, "stream-1", "aabbccddeeff001122")
	segDir := filepath.Join(recordingDir, "segments")
	if err := os.MkdirAll(segDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(recordingDir, "aabbccddeeff001122.m3u8"), make([]byte, 512), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(segDir, "chunk000.ts"), make([]byte, 5000), 0644); err != nil {
		t.Fatal(err)
	}

	sm.requestCanDelete = func(_ context.Context, _ string) (bool, string, int64, error) {
		return true, "synced", int64(10000), nil
	}

	var deletedAssetType string
	sm.sendArtifactDeleted = func(_, _, _, assetType string, _ uint64) error {
		deletedAssetType = assetType
		return nil
	}

	// clips dir is empty, DVR candidates will be picked up
	clipsDir := filepath.Join(sm.basePath, "clips")
	if err := os.MkdirAll(clipsDir, 0755); err != nil {
		t.Fatal(err)
	}

	totalBytes := uint64(100000)
	usedBytes := uint64(90000)

	err := sm.fallbackCleanup(clipsDir, usedBytes, totalBytes)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// DVR directory should be removed
	if _, err := os.Stat(recordingDir); !os.IsNotExist(err) {
		t.Fatal("expected DVR directory to be removed")
	}
	if deletedAssetType != "dvr" {
		t.Fatalf("expected asset type 'dvr', got %s", deletedAssetType)
	}
}

package control

import (
	"context"
	ipcpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/ipc"
	"testing"
	"time"
)

// --- SendFreezeComplete ---

func TestSendFreezeComplete_Connected(t *testing.T) {
	stream := &fakeControlStream{}
	storeConn(stream, "test-node")
	t.Cleanup(clearConn)

	err := SendFreezeComplete("req-1", "hash-abc", "completed", "s3://bucket/key", 4096, "", false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(stream.sent) != 1 {
		t.Fatalf("expected 1 message, got %d", len(stream.sent))
	}

	fc := stream.sent[0].GetFreezeComplete()
	if fc == nil {
		t.Fatal("expected FreezeComplete payload")
	}
	if fc.RequestId != "req-1" {
		t.Fatalf("expected request ID 'req-1', got %q", fc.RequestId)
	}
	if fc.AssetHash != "hash-abc" {
		t.Fatalf("expected asset hash 'hash-abc', got %q", fc.AssetHash)
	}
	if fc.Status != "completed" {
		t.Fatalf("expected status 'completed', got %q", fc.Status)
	}
	if fc.S3Url != "s3://bucket/key" {
		t.Fatalf("expected S3 URL, got %q", fc.S3Url)
	}
	if fc.SizeBytes != 4096 {
		t.Fatalf("expected 4096 bytes, got %d", fc.SizeBytes)
	}
	if fc.Error != "" {
		t.Fatalf("expected empty error, got %q", fc.Error)
	}
}

func TestSendFreezeComplete_WithError(t *testing.T) {
	stream := &fakeControlStream{}
	storeConn(stream, "test-node")
	t.Cleanup(clearConn)

	err := SendFreezeComplete("req-2", "hash-xyz", "failed", "", 0, "upload failed", false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	fc := stream.sent[0].GetFreezeComplete()
	if fc.Status != "failed" {
		t.Fatalf("expected status 'failed', got %q", fc.Status)
	}
	if fc.Error != "upload failed" {
		t.Fatalf("expected error msg, got %q", fc.Error)
	}
	if fc.LocalMissing {
		t.Fatalf("expected LocalMissing=false for transient error")
	}
}

func TestSendFreezeComplete_LocalMissingPropagates(t *testing.T) {
	stream := &fakeControlStream{}
	storeConn(stream, "test-node")
	t.Cleanup(clearConn)

	err := SendFreezeComplete("req-3", "hash-gone", "failed", "", 0, "open: no such file", true)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	fc := stream.sent[0].GetFreezeComplete()
	if !fc.LocalMissing {
		t.Fatalf("expected LocalMissing=true to propagate to wire")
	}
}

// --- SendSyncComplete ---

func TestSendSyncComplete_Connected(t *testing.T) {
	stream := &fakeControlStream{}
	storeConn(stream, "test-node")
	t.Cleanup(clearConn)

	err := SendSyncComplete("req-3", "hash-sync", "synced", "s3://bucket/synced", 8192, "", true, false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	sc := stream.sent[0].GetSyncComplete()
	if sc == nil {
		t.Fatal("expected SyncComplete payload")
	}
	if sc.RequestId != "req-3" {
		t.Fatalf("expected request ID, got %q", sc.RequestId)
	}
	if sc.AssetHash != "hash-sync" {
		t.Fatalf("expected hash, got %q", sc.AssetHash)
	}
	if sc.NodeId != "test-node" {
		t.Fatalf("expected node ID 'test-node', got %q", sc.NodeId)
	}
	if !sc.DtshIncluded {
		t.Fatal("expected dtsh_included=true")
	}
	if sc.SizeBytes != 8192 {
		t.Fatalf("expected 8192, got %d", sc.SizeBytes)
	}
}

func TestSendSyncComplete_DtshFalse(t *testing.T) {
	stream := &fakeControlStream{}
	storeConn(stream, "test-node")
	t.Cleanup(clearConn)

	_ = SendSyncComplete("req-4", "hash-no-dtsh", "synced", "s3://k", 1024, "", false, false)

	sc := stream.sent[0].GetSyncComplete()
	if sc.DtshIncluded {
		t.Fatal("expected dtsh_included=false")
	}
}

// --- SendStorageLifecycle ---

func TestSendStorageLifecycle_Connected(t *testing.T) {
	stream := &fakeControlStream{}
	storeConn(stream, "test-node")
	t.Cleanup(clearConn)

	data := &ipcpb.StorageLifecycleData{
		AssetHash: "hash-lc",
		Action:    ipcpb.StorageLifecycleData_ACTION_SYNC_STARTED,
		SizeBytes: 2048,
	}
	err := SendStorageLifecycle(data)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	msg := stream.sent[0]
	trigger := msg.GetMistTrigger()
	if trigger == nil {
		t.Fatal("expected MistTrigger payload")
	}
	if trigger.TriggerType != "storage_lifecycle" {
		t.Fatalf("expected storage_lifecycle, got %q", trigger.TriggerType)
	}
	if trigger.NodeId != "test-node" {
		t.Fatalf("expected node ID, got %q", trigger.NodeId)
	}
	slData := trigger.GetStorageLifecycleData()
	if slData == nil {
		t.Fatal("expected StorageLifecycleData payload")
	}
	if slData.AssetHash != "hash-lc" {
		t.Fatalf("expected hash, got %q", slData.AssetHash)
	}
	if slData.Action != ipcpb.StorageLifecycleData_ACTION_SYNC_STARTED {
		t.Fatalf("expected SYNC_STARTED, got %v", slData.Action)
	}
}

// --- SendFreezeProgress ---

func TestSendFreezeProgress_Connected(t *testing.T) {
	stream := &fakeControlStream{}
	storeConn(stream, "test-node")
	t.Cleanup(clearConn)

	err := SendFreezeProgress("req-6", "hash-fp", 75, 30000)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	fp := stream.sent[0].GetFreezeProgress()
	if fp == nil {
		t.Fatal("expected FreezeProgress payload")
	}
	if fp.RequestId != "req-6" {
		t.Fatalf("expected request ID, got %q", fp.RequestId)
	}
	if fp.Percent != 75 {
		t.Fatalf("expected 75%%, got %d", fp.Percent)
	}
	if fp.BytesUploaded != 30000 {
		t.Fatalf("expected 30000, got %d", fp.BytesUploaded)
	}
}

// --- SendArtifactDeleted ---

func TestSendArtifactDeleted_Connected(t *testing.T) {
	stream := &fakeControlStream{}
	storeConn(stream, "test-node")
	t.Cleanup(clearConn)

	err := SendArtifactDeleted("hash-del", "/data/clips/hash-del.mp4", "evicted", "clip", 32768)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	ad := stream.sent[0].GetArtifactDeleted()
	if ad == nil {
		t.Fatal("expected ArtifactDeleted payload")
	}
	if ad.ArtifactHash != "hash-del" {
		t.Fatalf("expected hash, got %q", ad.ArtifactHash)
	}
	if ad.ArtifactType != "clip" {
		t.Fatalf("expected clip, got %q", ad.ArtifactType)
	}
	if ad.Reason != "evicted" {
		t.Fatalf("expected evicted, got %q", ad.Reason)
	}
	if ad.SizeBytes != 32768 {
		t.Fatalf("expected 32768, got %d", ad.SizeBytes)
	}
	if ad.NodeId != "test-node" {
		t.Fatalf("expected node ID, got %q", ad.NodeId)
	}
}

// --- RequestFreezePermission ---

func TestRequestFreezePermission_Approved(t *testing.T) {
	stream := &fakeControlStream{}
	storeConn(stream, "test-node")
	t.Cleanup(clearConn)

	ctx := context.Background()
	done := make(chan struct{})
	var resp *ipcpb.FreezePermissionResponse
	var err error

	go func() {
		defer close(done)
		resp, err = RequestFreezePermission(ctx, "clip", "hash-perm", "/data/clips/hash.mp4", 4096, []string{"hash.mp4"})
	}()

	// Wait for the request to be sent
	deadline := time.After(2 * time.Second)
	for {
		stream.sendMu.Lock()
		n := len(stream.sent)
		stream.sendMu.Unlock()
		if n > 0 {
			break
		}
		select {
		case <-deadline:
			t.Fatal("timeout waiting for send")
		default:
			time.Sleep(5 * time.Millisecond)
		}
	}

	// Extract the requestID from the sent message
	fpReq := stream.sent[0].GetFreezePermissionRequest()
	if fpReq == nil {
		t.Fatal("expected FreezePermissionRequest")
	}
	reqID := fpReq.RequestId

	// Simulate Foghorn's response
	handleFreezePermissionResponse(&ipcpb.FreezePermissionResponse{
		RequestId:       reqID,
		Approved:        true,
		PresignedPutUrl: "https://s3.example.com/put?sig=abc",
	})

	waitForTestDone(t, done, "freeze permission request")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp == nil {
		t.Fatal("expected non-nil response")
	}
	if !resp.Approved {
		t.Fatal("expected approved=true")
	}
	if resp.PresignedPutUrl != "https://s3.example.com/put?sig=abc" {
		t.Fatalf("expected presigned URL, got %q", resp.PresignedPutUrl)
	}
}

func TestRequestFreezePermission_Timeout(t *testing.T) {
	stream := &fakeControlStream{}
	storeConn(stream, "test-node")
	t.Cleanup(clearConn)

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	_, err := RequestFreezePermission(ctx, "clip", "hash-timeout", "/path", 100, nil)
	if err == nil {
		t.Fatal("expected timeout error")
	}
}

func TestRequestFreezePermission_Disconnected(t *testing.T) {
	clearConn()

	_, err := RequestFreezePermission(context.Background(), "clip", "h", "/p", 100, nil)
	if err == nil {
		t.Fatal("expected error for disconnected stream")
	}
}

// --- RequestCanDelete ---

func TestRequestCanDelete_Safe(t *testing.T) {
	stream := &fakeControlStream{}
	storeConn(stream, "test-node")
	t.Cleanup(clearConn)

	ctx := context.Background()
	done := make(chan struct{})
	var safe bool
	var reason string
	var warmDur int64
	var err error

	go func() {
		defer close(done)
		safe, reason, warmDur, err = RequestCanDelete(ctx, "hash-can-del")
	}()

	// Wait for the request to be sent
	deadline := time.After(2 * time.Second)
	for {
		stream.sendMu.Lock()
		n := len(stream.sent)
		stream.sendMu.Unlock()
		if n > 0 {
			break
		}
		select {
		case <-deadline:
			t.Fatal("timeout waiting for send")
		default:
			time.Sleep(5 * time.Millisecond)
		}
	}

	// Simulate Foghorn's response
	handleCanDeleteResponse(&ipcpb.CanDeleteResponse{
		AssetHash:      "hash-can-del",
		SafeToDelete:   true,
		Reason:         "synced_to_s3",
		WarmDurationMs: 3600000,
	})

	waitForTestDone(t, done, "can-delete safe request")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !safe {
		t.Fatal("expected safe=true")
	}
	if reason != "synced_to_s3" {
		t.Fatalf("expected reason, got %q", reason)
	}
	if warmDur != 3600000 {
		t.Fatalf("expected 3600000 ms, got %d", warmDur)
	}
}

func TestRequestCanDelete_NotSafe(t *testing.T) {
	stream := &fakeControlStream{}
	storeConn(stream, "test-node")
	t.Cleanup(clearConn)

	ctx := context.Background()
	done := make(chan struct{})
	var safe bool
	var reason string
	var err error

	go func() {
		defer close(done)
		safe, reason, _, err = RequestCanDelete(ctx, "hash-not-safe")
	}()

	deadline := time.After(2 * time.Second)
	for {
		stream.sendMu.Lock()
		n := len(stream.sent)
		stream.sendMu.Unlock()
		if n > 0 {
			break
		}
		select {
		case <-deadline:
			t.Fatal("timeout waiting for send")
		default:
			time.Sleep(5 * time.Millisecond)
		}
	}

	handleCanDeleteResponse(&ipcpb.CanDeleteResponse{
		AssetHash:    "hash-not-safe",
		SafeToDelete: false,
		Reason:       "not_synced",
	})

	waitForTestDone(t, done, "can-delete unsafe request")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if safe {
		t.Fatal("expected safe=false")
	}
	if reason != "not_synced" {
		t.Fatalf("expected reason, got %q", reason)
	}
}

func TestRequestCanDelete_Disconnected(t *testing.T) {
	clearConn()
	_, _, _, err := RequestCanDelete(context.Background(), "hash-dc")
	if err == nil {
		t.Fatal("expected error")
	}
}

// --- ValidateEdgeToken ---

func TestValidateEdgeToken_Fresh(t *testing.T) {
	stream := &fakeControlStream{}
	storeConn(stream, "test-node")
	t.Cleanup(func() {
		clearConn()
		edgeTokenCache.Delete("tok-fresh")
	})

	ctx := context.Background()
	done := make(chan struct{})
	var resp *ipcpb.ValidateEdgeTokenResponse
	var err error

	go func() {
		defer close(done)
		resp, err = ValidateEdgeToken(ctx, "tok-fresh")
	}()

	deadline := time.After(2 * time.Second)
	for {
		stream.sendMu.Lock()
		n := len(stream.sent)
		stream.sendMu.Unlock()
		if n > 0 {
			break
		}
		select {
		case <-deadline:
			t.Fatal("timeout waiting for send")
		default:
			time.Sleep(5 * time.Millisecond)
		}
	}

	// Get the requestID from the sent message
	reqID := stream.sent[0].RequestId

	handleValidateEdgeTokenResponse(reqID, &ipcpb.ValidateEdgeTokenResponse{
		Valid:    true,
		TenantId: "tenant-abc",
	})

	waitForTestDone(t, done, "edge token validation request")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !resp.Valid {
		t.Fatal("expected valid=true")
	}
	if resp.TenantId != "tenant-abc" {
		t.Fatalf("expected tenant-abc, got %q", resp.TenantId)
	}
}

func TestValidateEdgeToken_Cached(t *testing.T) {
	t.Cleanup(func() {
		edgeTokenCache.Delete("tok-cached")
	})

	// Pre-populate cache
	edgeTokenCache.Store("tok-cached", &edgeTokenResult{
		resp: &ipcpb.ValidateEdgeTokenResponse{
			Valid:    true,
			TenantId: "tenant-cached",
		},
		expiresAt: time.Now().Add(5 * time.Minute),
	})

	// No stream needed — should return from cache
	clearConn()

	resp, err := ValidateEdgeToken(context.Background(), "tok-cached")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !resp.Valid {
		t.Fatal("expected valid=true from cache")
	}
	if resp.TenantId != "tenant-cached" {
		t.Fatalf("expected tenant-cached, got %q", resp.TenantId)
	}
}

func TestValidateEdgeToken_CacheExpired(t *testing.T) {
	t.Cleanup(func() {
		edgeTokenCache.Delete("tok-expired")
	})

	// Expired cache entry
	edgeTokenCache.Store("tok-expired", &edgeTokenResult{
		resp:      &ipcpb.ValidateEdgeTokenResponse{Valid: true},
		expiresAt: time.Now().Add(-1 * time.Minute),
	})

	// No stream → should fail (cache expired, no connection)
	clearConn()

	_, err := ValidateEdgeToken(context.Background(), "tok-expired")
	if err == nil {
		t.Fatal("expected error for expired cache + disconnected")
	}
}

func TestValidateEdgeToken_Disconnected(t *testing.T) {
	clearConn()
	edgeTokenCache.Delete("tok-dc")

	_, err := ValidateEdgeToken(context.Background(), "tok-dc")
	if err == nil {
		t.Fatal("expected error for disconnected stream")
	}
}

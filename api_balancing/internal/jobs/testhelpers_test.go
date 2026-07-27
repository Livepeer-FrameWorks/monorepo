package jobs

import (
	"context"
	"errors"
	commodorepb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/commodore"
	ipcpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/ipc"
	"strings"
	"sync"
	"time"
)

// mockReconcilerS3Client implements ReconcilerS3Client for testing.
type mockReconcilerS3Client struct {
	mu sync.Mutex

	generatePresignedPUTFn func(key string, expiry time.Duration) (string, error)
	buildClipS3KeyFn       func(tenantID, streamName, clipHash, format string) string
	buildDVRS3KeyFn        func(tenantID, internalName, dvrHash string) string
	buildVodS3KeyFn        func(tenantID, artifactHash, filename string) string
	parseS3URLFn           func(s3URL string) (string, error)

	presignedPUTCalls []presignedPUTCall
	clipKeyCalls      []clipKeyCall
	dvrKeyCalls       []dvrKeyCall
	vodKeyCalls       []vodKeyCall
}

type presignedPUTCall struct {
	Key    string
	Expiry time.Duration
}
type clipKeyCall struct {
	TenantID, StreamName, ClipHash, Format string
}
type dvrKeyCall struct {
	TenantID, InternalName, DVRHash string
}
type vodKeyCall struct {
	TenantID, ArtifactHash, Filename string
}

func (m *mockReconcilerS3Client) GeneratePresignedPUT(key string, expiry time.Duration) (string, error) {
	m.mu.Lock()
	m.presignedPUTCalls = append(m.presignedPUTCalls, presignedPUTCall{key, expiry})
	m.mu.Unlock()
	if m.generatePresignedPUTFn != nil {
		return m.generatePresignedPUTFn(key, expiry)
	}
	return "https://s3.example.com/presigned/" + key, nil
}

func (m *mockReconcilerS3Client) Delete(_ context.Context, _ string) error { return nil }

func (m *mockReconcilerS3Client) ParseLocalS3URL(s3URL string) (string, error) {
	if m.parseS3URLFn != nil {
		return m.parseS3URLFn(s3URL)
	}
	// Default: ONLY the conventional test bucket "bucket" is local; a foreign bucket errors (mirrors the real
	// client's bucket guard, so tests can exercise the remote-row skip).
	if strings.HasPrefix(s3URL, "s3://bucket/") {
		return strings.TrimPrefix(s3URL, "s3://bucket/"), nil
	}
	return "", errors.New("not a local s3 URL: " + s3URL)
}

func (m *mockReconcilerS3Client) BuildClipS3Key(tenantID, streamName, clipHash, format string) string {
	m.mu.Lock()
	m.clipKeyCalls = append(m.clipKeyCalls, clipKeyCall{tenantID, streamName, clipHash, format})
	m.mu.Unlock()
	if m.buildClipS3KeyFn != nil {
		return m.buildClipS3KeyFn(tenantID, streamName, clipHash, format)
	}
	return tenantID + "/" + streamName + "/clips/" + clipHash + "." + format
}

func (m *mockReconcilerS3Client) BuildDVRS3Key(tenantID, internalName, dvrHash string) string {
	m.mu.Lock()
	m.dvrKeyCalls = append(m.dvrKeyCalls, dvrKeyCall{tenantID, internalName, dvrHash})
	m.mu.Unlock()
	if m.buildDVRS3KeyFn != nil {
		return m.buildDVRS3KeyFn(tenantID, internalName, dvrHash)
	}
	return tenantID + "/" + internalName + "/dvr/" + dvrHash
}

func (m *mockReconcilerS3Client) BuildVodS3Key(tenantID, artifactHash, filename string) string {
	m.mu.Lock()
	m.vodKeyCalls = append(m.vodKeyCalls, vodKeyCall{tenantID, artifactHash, filename})
	m.mu.Unlock()
	if m.buildVodS3KeyFn != nil {
		return m.buildVodS3KeyFn(tenantID, artifactHash, filename)
	}
	return tenantID + "/vods/" + artifactHash + "/" + filename
}

// mockCommodoreClient implements ReconcilerCommodoreClient for testing.
type mockCommodoreClient struct {
	mu sync.Mutex

	resolveClipHashFn func(ctx context.Context, hash string) (*commodorepb.ResolveClipHashResponse, error)
	resolveDVRHashFn  func(ctx context.Context, hash string) (*commodorepb.ResolveDVRHashResponse, error)
	resolveVodHashFn  func(ctx context.Context, hash string) (*commodorepb.ResolveVodHashResponse, error)

	clipCalls      []string
	dvrCalls       []string
	vodCalls       []string
	snapshotCalls  []*commodorepb.UpdateArtifactCatalogSnapshotRequest
	snapshotErr    error
	snapshotRespFn func(req *commodorepb.UpdateArtifactCatalogSnapshotRequest) (*commodorepb.UpdateArtifactCatalogSnapshotResponse, error)
}

func (m *mockCommodoreClient) ResolveClipHash(ctx context.Context, hash string) (*commodorepb.ResolveClipHashResponse, error) {
	m.mu.Lock()
	m.clipCalls = append(m.clipCalls, hash)
	m.mu.Unlock()
	if m.resolveClipHashFn != nil {
		return m.resolveClipHashFn(ctx, hash)
	}
	return &commodorepb.ResolveClipHashResponse{Found: false}, nil
}

func (m *mockCommodoreClient) ResolveDVRHash(ctx context.Context, hash string) (*commodorepb.ResolveDVRHashResponse, error) {
	m.mu.Lock()
	m.dvrCalls = append(m.dvrCalls, hash)
	m.mu.Unlock()
	if m.resolveDVRHashFn != nil {
		return m.resolveDVRHashFn(ctx, hash)
	}
	return &commodorepb.ResolveDVRHashResponse{Found: false}, nil
}

func (m *mockCommodoreClient) ResolveVodHash(ctx context.Context, hash string) (*commodorepb.ResolveVodHashResponse, error) {
	m.mu.Lock()
	m.vodCalls = append(m.vodCalls, hash)
	m.mu.Unlock()
	if m.resolveVodHashFn != nil {
		return m.resolveVodHashFn(ctx, hash)
	}
	return &commodorepb.ResolveVodHashResponse{Found: false}, nil
}

// UpdateArtifactCatalogSnapshot records the snapshot and, by default, confirms coverage by
// echoing the requested source revision back as current_revision (the found+covered case).
func (m *mockCommodoreClient) UpdateArtifactCatalogSnapshot(_ context.Context, req *commodorepb.UpdateArtifactCatalogSnapshotRequest) (*commodorepb.UpdateArtifactCatalogSnapshotResponse, error) {
	m.mu.Lock()
	m.snapshotCalls = append(m.snapshotCalls, req)
	m.mu.Unlock()
	if m.snapshotErr != nil {
		return nil, m.snapshotErr
	}
	if m.snapshotRespFn != nil {
		return m.snapshotRespFn(req)
	}
	return &commodorepb.UpdateArtifactCatalogSnapshotResponse{Found: true, CurrentRevision: req.GetSourceRevision()}, nil
}

// freezeCapture records calls to SendFreeze for assertion.
type freezeCapture struct {
	mu    sync.Mutex
	calls []freezeCall
	err   error
}

type freezeCall struct {
	NodeID string
	Req    *ipcpb.FreezeRequest
}

func (fc *freezeCapture) send(nodeID string, req *ipcpb.FreezeRequest) error {
	fc.mu.Lock()
	defer fc.mu.Unlock()
	fc.calls = append(fc.calls, freezeCall{nodeID, req})
	return fc.err
}

func (fc *freezeCapture) count() int {
	fc.mu.Lock()
	defer fc.mu.Unlock()
	return len(fc.calls)
}

func (fc *freezeCapture) last() freezeCall {
	fc.mu.Lock()
	defer fc.mu.Unlock()
	return fc.calls[len(fc.calls)-1]
}

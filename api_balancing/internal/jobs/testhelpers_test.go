package jobs

import (
	"context"
	"sync"
	"time"

	pb "frameworks/pkg/proto"
)

// mockReconcilerS3Client implements ReconcilerS3Client for testing.
type mockReconcilerS3Client struct {
	mu sync.Mutex

	generatePresignedPUTFn func(key string, expiry time.Duration) (string, error)
	buildClipS3KeyFn       func(tenantID, streamName, clipHash, format string) string
	buildDVRS3KeyFn        func(tenantID, internalName, dvrHash string) string
	buildVodS3KeyFn        func(tenantID, artifactHash, filename string) string

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

	resolveClipHashFn func(ctx context.Context, hash string) (*pb.ResolveClipHashResponse, error)
	resolveDVRHashFn  func(ctx context.Context, hash string) (*pb.ResolveDVRHashResponse, error)
	resolveVodHashFn  func(ctx context.Context, hash string) (*pb.ResolveVodHashResponse, error)

	clipCalls []string
	dvrCalls  []string
	vodCalls  []string
}

func (m *mockCommodoreClient) ResolveClipHash(ctx context.Context, hash string) (*pb.ResolveClipHashResponse, error) {
	m.mu.Lock()
	m.clipCalls = append(m.clipCalls, hash)
	m.mu.Unlock()
	if m.resolveClipHashFn != nil {
		return m.resolveClipHashFn(ctx, hash)
	}
	return &pb.ResolveClipHashResponse{Found: false}, nil
}

func (m *mockCommodoreClient) ResolveDVRHash(ctx context.Context, hash string) (*pb.ResolveDVRHashResponse, error) {
	m.mu.Lock()
	m.dvrCalls = append(m.dvrCalls, hash)
	m.mu.Unlock()
	if m.resolveDVRHashFn != nil {
		return m.resolveDVRHashFn(ctx, hash)
	}
	return &pb.ResolveDVRHashResponse{Found: false}, nil
}

func (m *mockCommodoreClient) ResolveVodHash(ctx context.Context, hash string) (*pb.ResolveVodHashResponse, error) {
	m.mu.Lock()
	m.vodCalls = append(m.vodCalls, hash)
	m.mu.Unlock()
	if m.resolveVodHashFn != nil {
		return m.resolveVodHashFn(ctx, hash)
	}
	return &pb.ResolveVodHashResponse{Found: false}, nil
}

// freezeCapture records calls to SendFreeze for assertion.
type freezeCapture struct {
	mu    sync.Mutex
	calls []freezeCall
	err   error
}

type freezeCall struct {
	NodeID string
	Req    *pb.FreezeRequest
}

func (fc *freezeCapture) send(nodeID string, req *pb.FreezeRequest) error {
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

package control

import (
	"context"
	"database/sql"
	"sync"
	"testing"
	"time"

	"frameworks/api_balancing/internal/state"
	pb "frameworks/pkg/proto"

	"github.com/DATA-DOG/go-sqlmock"
)

// mockS3Client implements S3ClientInterface for testing.
type mockS3Client struct {
	mu sync.Mutex

	generatePresignedPUTFn func(key string, expiry time.Duration) (string, error)
	generatePresignedGETFn func(key string, expiry time.Duration) (string, error)
	listPrefixFn           func(ctx context.Context, prefix string) ([]string, error)
	deleteFn               func(ctx context.Context, key string) error
	deleteByURLFn          func(ctx context.Context, s3URL string) error
	deletePrefixFn         func(ctx context.Context, prefix string) (int, error)
	buildClipS3KeyFn       func(tenantID, streamName, clipHash, format string) string
	buildDVRS3KeyFn        func(tenantID, internalName, dvrHash string) string
	buildVodS3KeyFn        func(tenantID, artifactHash, filename string) string
	buildS3URLFn           func(key string) string

	presignPUTCalls   []string
	presignGETCalls   []string
	deleteCalls       []string
	deleteByURLCalls  []string
	deletePrefixCalls []string
	clipKeyCalls      int
	dvrKeyCalls       int
	vodKeyCalls       int
}

func (m *mockS3Client) GeneratePresignedPUT(key string, expiry time.Duration) (string, error) {
	m.mu.Lock()
	m.presignPUTCalls = append(m.presignPUTCalls, key)
	m.mu.Unlock()
	if m.generatePresignedPUTFn != nil {
		return m.generatePresignedPUTFn(key, expiry)
	}
	return "https://s3.test/put/" + key, nil
}

func (m *mockS3Client) GeneratePresignedGET(key string, expiry time.Duration) (string, error) {
	m.mu.Lock()
	m.presignGETCalls = append(m.presignGETCalls, key)
	m.mu.Unlock()
	if m.generatePresignedGETFn != nil {
		return m.generatePresignedGETFn(key, expiry)
	}
	return "https://s3.test/get/" + key, nil
}

func (m *mockS3Client) ListPrefix(ctx context.Context, prefix string) ([]string, error) {
	if m.listPrefixFn != nil {
		return m.listPrefixFn(ctx, prefix)
	}
	return nil, nil
}

func (m *mockS3Client) Delete(ctx context.Context, key string) error {
	m.mu.Lock()
	m.deleteCalls = append(m.deleteCalls, key)
	m.mu.Unlock()
	if m.deleteFn != nil {
		return m.deleteFn(ctx, key)
	}
	return nil
}

func (m *mockS3Client) DeleteByURL(ctx context.Context, s3URL string) error {
	m.mu.Lock()
	m.deleteByURLCalls = append(m.deleteByURLCalls, s3URL)
	m.mu.Unlock()
	if m.deleteByURLFn != nil {
		return m.deleteByURLFn(ctx, s3URL)
	}
	return nil
}

func (m *mockS3Client) DeletePrefix(ctx context.Context, prefix string) (int, error) {
	m.mu.Lock()
	m.deletePrefixCalls = append(m.deletePrefixCalls, prefix)
	m.mu.Unlock()
	if m.deletePrefixFn != nil {
		return m.deletePrefixFn(ctx, prefix)
	}
	return 0, nil
}

func (m *mockS3Client) BuildClipS3Key(tenantID, streamName, clipHash, format string) string {
	m.mu.Lock()
	m.clipKeyCalls++
	m.mu.Unlock()
	if m.buildClipS3KeyFn != nil {
		return m.buildClipS3KeyFn(tenantID, streamName, clipHash, format)
	}
	return tenantID + "/" + streamName + "/clips/" + clipHash + "." + format
}

func (m *mockS3Client) BuildDVRS3Key(tenantID, internalName, dvrHash string) string {
	m.mu.Lock()
	m.dvrKeyCalls++
	m.mu.Unlock()
	if m.buildDVRS3KeyFn != nil {
		return m.buildDVRS3KeyFn(tenantID, internalName, dvrHash)
	}
	return tenantID + "/" + internalName + "/dvr/" + dvrHash
}

func (m *mockS3Client) BuildVodS3Key(tenantID, artifactHash, filename string) string {
	m.mu.Lock()
	m.vodKeyCalls++
	m.mu.Unlock()
	if m.buildVodS3KeyFn != nil {
		return m.buildVodS3KeyFn(tenantID, artifactHash, filename)
	}
	return tenantID + "/vods/" + artifactHash + "/" + filename
}

func (m *mockS3Client) BuildS3URL(key string) string {
	if m.buildS3URLFn != nil {
		return m.buildS3URLFn(key)
	}
	return "s3://bucket/" + key
}

// mockArtifactRepo implements state.ArtifactRepository for testing.
type mockArtifactRepo struct {
	mu sync.Mutex

	setSyncStatusFn         func(ctx context.Context, hash, status, s3URL string) error
	addCachedNodeFn         func(ctx context.Context, hash, nodeID string) error
	addCachedNodeWithPathFn func(ctx context.Context, hash, nodeID, path string, size int64) error
	markNodeOrphanedFn      func(ctx context.Context, nodeID string) error

	syncStatusCalls        []syncStatusCall
	addCachedNodeCalls     []cachedNodeCall
	addCachedNodePathCalls []cachedNodePathCall
	markOrphanedCalls      []string
}

type syncStatusCall struct {
	Hash, Status, S3URL string
}
type cachedNodeCall struct {
	Hash, NodeID string
}
type cachedNodePathCall struct {
	Hash, NodeID, Path string
	Size               int64
}

func (m *mockArtifactRepo) UpsertArtifacts(_ context.Context, _ string, _ []state.ArtifactRecord) error {
	return nil
}

func (m *mockArtifactRepo) GetArtifactSyncInfo(_ context.Context, _ string) (*state.ArtifactSyncInfo, error) {
	return nil, nil
}

func (m *mockArtifactRepo) SetSyncStatus(ctx context.Context, hash, status, s3URL string) error {
	m.mu.Lock()
	m.syncStatusCalls = append(m.syncStatusCalls, syncStatusCall{hash, status, s3URL})
	m.mu.Unlock()
	if m.setSyncStatusFn != nil {
		return m.setSyncStatusFn(ctx, hash, status, s3URL)
	}
	return nil
}

func (m *mockArtifactRepo) AddCachedNode(ctx context.Context, hash, nodeID string) error {
	m.mu.Lock()
	m.addCachedNodeCalls = append(m.addCachedNodeCalls, cachedNodeCall{hash, nodeID})
	m.mu.Unlock()
	if m.addCachedNodeFn != nil {
		return m.addCachedNodeFn(ctx, hash, nodeID)
	}
	return nil
}

func (m *mockArtifactRepo) AddCachedNodeWithPath(ctx context.Context, hash, nodeID, path string, size int64) error {
	m.mu.Lock()
	m.addCachedNodePathCalls = append(m.addCachedNodePathCalls, cachedNodePathCall{hash, nodeID, path, size})
	m.mu.Unlock()
	if m.addCachedNodeWithPathFn != nil {
		return m.addCachedNodeWithPathFn(ctx, hash, nodeID, path, size)
	}
	return nil
}

func (m *mockArtifactRepo) IsSynced(_ context.Context, _ string) (bool, error) {
	return false, nil
}

func (m *mockArtifactRepo) GetCachedAt(_ context.Context, _ string) (int64, error) {
	return 0, nil
}

func (m *mockArtifactRepo) ListAllNodeArtifacts(_ context.Context) (map[string][]state.ArtifactRecord, error) {
	return nil, nil
}

func (m *mockArtifactRepo) MarkNodeArtifactsOrphaned(ctx context.Context, nodeID string) error {
	m.mu.Lock()
	m.markOrphanedCalls = append(m.markOrphanedCalls, nodeID)
	m.mu.Unlock()
	if m.markNodeOrphanedFn != nil {
		return m.markNodeOrphanedFn(ctx, nodeID)
	}
	return nil
}

func (m *mockArtifactRepo) NeedsDtshSync(_ context.Context, _ string) bool {
	return false
}

func (m *mockArtifactRepo) UpdateDVRProgressByHash(_ context.Context, _ string, _ string, _ int64) error {
	return nil
}

func (m *mockArtifactRepo) UpdateDVRCompletionByHash(_ context.Context, _ string, _ string, _ int64, _ int64, _ string, _ string) error {
	return nil
}

// setupArtifactTestDeps swaps package-level vars (db, s3Client, artifactRepo) and returns
// the sqlmock handle plus mock objects. Restores originals on t.Cleanup.
func setupArtifactTestDeps(t *testing.T) (sqlmock.Sqlmock, *mockS3Client, *mockArtifactRepo) {
	t.Helper()

	mockDB, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}

	prevDB := db
	prevS3 := s3Client
	prevRepo := artifactRepo

	s3Mock := &mockS3Client{}
	repoMock := &mockArtifactRepo{}

	db = mockDB
	s3Client = s3Mock
	artifactRepo = repoMock

	sm := state.ResetDefaultManagerForTests()

	t.Cleanup(func() {
		db = prevDB
		s3Client = prevS3
		artifactRepo = prevRepo
		sm.Shutdown()
		mockDB.Close()
	})

	return mock, s3Mock, repoMock
}

// setupArtifactTestDepsWithDB is like setupArtifactTestDeps but also returns the *sql.DB.
func setupArtifactTestDepsWithDB(t *testing.T) (*sql.DB, sqlmock.Sqlmock, *mockS3Client, *mockArtifactRepo) {
	t.Helper()

	mockDB, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}

	prevDB := db
	prevS3 := s3Client
	prevRepo := artifactRepo

	s3Mock := &mockS3Client{}
	repoMock := &mockArtifactRepo{}

	db = mockDB
	s3Client = s3Mock
	artifactRepo = repoMock

	sm := state.ResetDefaultManagerForTests()

	t.Cleanup(func() {
		db = prevDB
		s3Client = prevS3
		artifactRepo = prevRepo
		sm.Shutdown()
		mockDB.Close()
	})

	return mockDB, mock, s3Mock, repoMock
}

// captureStream is a fake HelmsmanControl_ConnectServer that captures sent messages.
type captureStream struct {
	pb.HelmsmanControl_ConnectServer
	mu   sync.Mutex
	sent []*pb.ControlMessage
}

func (cs *captureStream) Send(msg *pb.ControlMessage) error {
	cs.mu.Lock()
	defer cs.mu.Unlock()
	cs.sent = append(cs.sent, msg)
	return nil
}

func (cs *captureStream) lastSent() *pb.ControlMessage {
	cs.mu.Lock()
	defer cs.mu.Unlock()
	if len(cs.sent) == 0 {
		return nil
	}
	return cs.sent[len(cs.sent)-1]
}

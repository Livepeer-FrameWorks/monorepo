package federation

import (
	"context"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"frameworks/api_balancing/internal/state"
	"frameworks/pkg/logging"
	pb "frameworks/pkg/proto"
)

// fakeMintS3Client captures presigned PUT calls so the tests can assert
// the exact key shapes the federated mint produced — these MUST match
// what the local-mint code paths would have built for the same inputs.
type fakeMintS3Client struct {
	putCalls []putCall

	// Optional override: when non-nil, GeneratePresignedPUT returns the
	// (url, err) per key from this map; missing keys fall through to the
	// default deterministic URL.
	putByKey map[string]struct {
		url string
		err error
	}
}

type putCall struct {
	key string
}

func (f *fakeMintS3Client) GeneratePresignedGET(string, time.Duration) (string, error) {
	return "", nil
}
func (f *fakeMintS3Client) GeneratePresignedPUT(key string, _ time.Duration) (string, error) {
	f.putCalls = append(f.putCalls, putCall{key: key})
	if entry, ok := f.putByKey[key]; ok {
		return entry.url, entry.err
	}
	return "https://s3.example.com/" + key + "?X-Amz-Signature=abc", nil
}
func (f *fakeMintS3Client) GeneratePresignedURLsForDVR(string, bool, time.Duration) (map[string]string, error) {
	return nil, nil
}
func (f *fakeMintS3Client) BuildClipS3Key(tenantID, streamName, clipHash, format string) string {
	return "clips/" + tenantID + "/" + streamName + "/" + clipHash + "." + format
}
func (f *fakeMintS3Client) BuildDVRS3Key(tenantID, internalName, dvrHash string) string {
	return "dvr/" + tenantID + "/" + internalName + "/" + dvrHash + "/"
}
func (f *fakeMintS3Client) BuildVodS3Key(tenantID, artifactHash, filename string) string {
	return "vod/" + tenantID + "/" + artifactHash + "/" + filename
}

// mintTestServer builds a FederationServer that owns the named target
// cluster's storage by default.
func mintTestServer(t *testing.T, fake *fakeMintS3Client, db *sqlmock.Sqlmock) *FederationServer {
	t.Helper()
	cfg := FederationServerConfig{
		Logger:    logging.NewLogger(),
		ClusterID: "platform-eu",
		S3Client:  fake,
		LocalS3Backing: S3Backing{
			Bucket:   "frameworks",
			Endpoint: "https://s3.example.com",
			Region:   "us-east-1",
		},
		IsServedCluster: func(id string) bool { return id == "platform-eu" },
		AdvertisedBacking: func(_ context.Context, _, clusterID string) (S3Backing, bool) {
			if clusterID == "platform-eu" {
				return S3Backing{Bucket: "frameworks", Endpoint: "https://s3.example.com", Region: "us-east-1"}, true
			}
			return S3Backing{}, false
		},
	}
	return NewFederationServer(cfg)
}

func TestMintStorageURLs_RequiresAuth(t *testing.T) {
	srv := mintTestServer(t, &fakeMintS3Client{}, nil)
	_, err := srv.MintStorageURLs(context.Background(), &pb.MintStorageURLsRequest{
		TenantId:        "tenant-a",
		TargetClusterId: "platform-eu",
		ArtifactType:    "thumbnail",
		ArtifactKey:     "stream-uuid/poster.jpg",
		Op:              pb.MintStorageURLsRequest_OPERATION_PUT_SINGLE,
	})
	if status.Code(err) != codes.PermissionDenied {
		t.Fatalf("expected PermissionDenied without service auth, got %v", err)
	}
}

func TestMintStorageURLs_RejectsTargetWeDoNotOwn(t *testing.T) {
	srv := mintTestServer(t, &fakeMintS3Client{}, nil)
	resp, err := srv.MintStorageURLs(serviceAuthContext(), &pb.MintStorageURLsRequest{
		TenantId:        "tenant-a",
		TargetClusterId: "selfhost-x", // not served + not advertised here
		ArtifactType:    "thumbnail",
		ArtifactKey:     "stream-uuid/poster.jpg",
		Op:              pb.MintStorageURLsRequest_OPERATION_PUT_SINGLE,
	})
	if err != nil {
		t.Fatalf("unexpected RPC error: %v", err)
	}
	if resp.GetAccepted() {
		t.Fatal("must reject when target_cluster_id is not locally owned")
	}
	if resp.GetReason() != "storage_not_owned_here" {
		t.Fatalf("expected reason=storage_not_owned_here, got %q", resp.GetReason())
	}
}

func TestMintStorageURLs_LiveThumbnail_HappyPath(t *testing.T) {
	state.ResetDefaultManagerForTests()
	t.Cleanup(func() { state.ResetDefaultManagerForTests() })

	sm := state.DefaultManager()
	if err := sm.UpdateStreamFromBuffer("stream-a", "stream-a", "node-a", "tenant-a", "FULL", ""); err != nil {
		t.Fatalf("seed stream: %v", err)
	}

	fake := &fakeMintS3Client{}
	srv := mintTestServer(t, fake, nil)

	resp, err := srv.MintStorageURLs(serviceAuthContext(), &pb.MintStorageURLsRequest{
		TenantId:           "tenant-a",
		TargetClusterId:    "platform-eu",
		ArtifactType:       "thumbnail",
		ArtifactKey:        "stream-uuid/poster.jpg",
		Op:                 pb.MintStorageURLsRequest_OPERATION_PUT_SINGLE,
		StreamInternalName: "stream-a",
		ContentType:        "image/jpeg",
	})
	if err != nil {
		t.Fatalf("unexpected RPC error: %v", err)
	}
	if !resp.GetAccepted() {
		t.Fatalf("expected acceptance; reason=%q", resp.GetReason())
	}
	wantKey := "thumbnails/stream-uuid/poster.jpg"
	if resp.GetS3Key() != wantKey {
		t.Fatalf("S3 key shape mismatch: got %q want %q", resp.GetS3Key(), wantKey)
	}
	if len(fake.putCalls) != 1 || fake.putCalls[0].key != wantKey {
		t.Fatalf("expected single PUT against %q, got %+v", wantKey, fake.putCalls)
	}
}

func TestMintStorageURLs_LiveThumbnail_TenantMismatch(t *testing.T) {
	state.ResetDefaultManagerForTests()
	t.Cleanup(func() { state.ResetDefaultManagerForTests() })

	sm := state.DefaultManager()
	if err := sm.UpdateStreamFromBuffer("stream-a", "stream-a", "node-a", "tenant-other", "FULL", ""); err != nil {
		t.Fatalf("seed stream: %v", err)
	}

	srv := mintTestServer(t, &fakeMintS3Client{}, nil)
	resp, err := srv.MintStorageURLs(serviceAuthContext(), &pb.MintStorageURLsRequest{
		TenantId:           "tenant-a", // doesn't match the stream's owner
		TargetClusterId:    "platform-eu",
		ArtifactType:       "thumbnail",
		ArtifactKey:        "stream-uuid/poster.jpg",
		Op:                 pb.MintStorageURLsRequest_OPERATION_PUT_SINGLE,
		StreamInternalName: "stream-a",
	})
	if err != nil {
		t.Fatalf("unexpected RPC error: %v", err)
	}
	if resp.GetAccepted() {
		t.Fatal("must reject on tenant mismatch")
	}
	if resp.GetReason() != "tenant_mismatch" {
		t.Fatalf("expected tenant_mismatch, got %q", resp.GetReason())
	}
}

func TestMintStorageURLs_VodRejectsMultipart(t *testing.T) {
	srv := mintTestServer(t, &fakeMintS3Client{}, nil)
	resp, err := srv.MintStorageURLs(serviceAuthContext(), &pb.MintStorageURLsRequest{
		TenantId:        "tenant-a",
		TargetClusterId: "platform-eu",
		ArtifactType:    "vod",
		ArtifactKey:     "abcd1234",
		Op:              pb.MintStorageURLsRequest_OPERATION_PUT_DVR_SET, // wrong op for vod
	})
	if err != nil {
		t.Fatalf("unexpected RPC error: %v", err)
	}
	if resp.GetAccepted() {
		t.Fatal("must reject vod with non-single op")
	}
	if resp.GetReason() != "unsupported_operation" {
		t.Fatalf("expected unsupported_operation, got %q", resp.GetReason())
	}
}

func TestMintStorageURLs_DvrSet_RequiresFilenames(t *testing.T) {
	srv := mintTestServer(t, &fakeMintS3Client{}, nil)
	resp, err := srv.MintStorageURLs(serviceAuthContext(), &pb.MintStorageURLsRequest{
		TenantId:        "tenant-a",
		TargetClusterId: "platform-eu",
		ArtifactType:    "dvr",
		ArtifactKey:     "abcd1234",
		Op:              pb.MintStorageURLsRequest_OPERATION_PUT_DVR_SET,
		// SegmentFilenames intentionally empty
	})
	if err != nil {
		t.Fatalf("unexpected RPC error: %v", err)
	}
	if resp.GetAccepted() {
		t.Fatal("must reject dvr set without filenames")
	}
	if resp.GetReason() != "unsupported_operation" {
		t.Fatalf("expected unsupported_operation, got %q", resp.GetReason())
	}
}

func TestPrepareArtifact_RedirectsWhenStorageOwnedElsewhere(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer db.Close()

	rows := sqlmock.NewRows([]string{"internal_name", "stream_internal_name", "artifact_type", "format", "storage_location", "sync_status", "size_bytes", "authoritative_cluster"}).
		AddRow("clip-x", "stream-x", "clip", "mp4", "s3", "synced", 1024, "selfhost-foreign")
	mock.ExpectQuery("FROM foghorn.artifacts").WillReturnRows(rows)

	cfg := FederationServerConfig{
		Logger:          logging.NewLogger(),
		ClusterID:       "platform-eu",
		DB:              db,
		S3Client:        &fakeMintS3Client{},
		LocalS3Backing:  S3Backing{Bucket: "frameworks", Region: "us-east-1"},
		IsServedCluster: func(id string) bool { return id == "platform-eu" },
		AdvertisedBacking: func(_ context.Context, _, clusterID string) (S3Backing, bool) {
			if clusterID == "platform-eu" {
				return S3Backing{Bucket: "frameworks", Region: "us-east-1"}, true
			}
			return S3Backing{}, false
		},
	}
	srv := NewFederationServer(cfg)

	resp, err := srv.PrepareArtifact(serviceAuthContext(), &pb.PrepareArtifactRequest{
		ArtifactId: "hash-x",
		TenantId:   "tenant-a",
	})
	if err != nil {
		t.Fatalf("unexpected RPC error: %v", err)
	}
	// The artifact's authoritative cluster (selfhost-foreign) is NOT
	// served locally, so PrepareArtifact must emit redirect_cluster_id
	// instead of attempting a presigned mint. Other response fields are
	// ignored when redirect is set.
	if got := resp.GetRedirectClusterId(); got != "selfhost-foreign" {
		t.Fatalf("expected redirect to selfhost-foreign, got %q", got)
	}
	if resp.GetUrl() != "" || resp.GetReady() {
		t.Fatalf("redirect response must not carry a presigned URL or ready=true: %+v", resp)
	}
}

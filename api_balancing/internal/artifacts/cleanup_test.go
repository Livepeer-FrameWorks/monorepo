package artifacts

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	pb "frameworks/pkg/proto"
)

type fakeS3 struct {
	deleteCalls       []string
	deletePrefixCalls []string
	deleteErr         error
	deletePrefixErr   error
}

func (f *fakeS3) Delete(_ context.Context, key string) error {
	f.deleteCalls = append(f.deleteCalls, key)
	return f.deleteErr
}
func (f *fakeS3) DeletePrefix(_ context.Context, prefix string) (int, error) {
	f.deletePrefixCalls = append(f.deletePrefixCalls, prefix)
	return 0, f.deletePrefixErr
}
func (f *fakeS3) ParseS3URL(s3URL string) (string, error) {
	rest, ok := strings.CutPrefix(s3URL, "s3://")
	if !ok {
		return "", fmt.Errorf("not an s3:// URL: %s", s3URL)
	}
	_, key, ok := strings.Cut(rest, "/")
	if !ok {
		return "", fmt.Errorf("no key in URL")
	}
	return key, nil
}

func TestCleaner_LocalClipUsesFormatColumn(t *testing.T) {
	s3 := &fakeS3{}
	c := &Cleaner{LocalCluster: "eu-west", S3: s3}

	err := c.Delete(context.Background(), ArtifactRef{
		Hash:           "clip-1",
		Type:           "clip",
		TenantID:       "tenant-a",
		StreamInternal: "stream-x",
		Format:         "webm",
	})
	if err != nil {
		t.Fatalf("Delete err = %v", err)
	}
	if len(s3.deleteCalls) != 1 {
		t.Fatalf("Delete calls = %d, want 1", len(s3.deleteCalls))
	}
	if got, want := s3.deleteCalls[0], "clips/tenant-a/stream-x/clip-1.webm"; got != want {
		t.Errorf("key = %q, want %q", got, want)
	}
}

func TestCleaner_LocalDVRUsesPrefix(t *testing.T) {
	s3 := &fakeS3{}
	c := &Cleaner{LocalCluster: "eu-west", S3: s3}

	err := c.Delete(context.Background(), ArtifactRef{
		Hash:           "dvr-1",
		Type:           "dvr",
		TenantID:       "tenant-a",
		StreamInternal: "stream-x",
	})
	if err != nil {
		t.Fatalf("Delete err = %v", err)
	}
	if len(s3.deletePrefixCalls) != 1 {
		t.Fatalf("DeletePrefix calls = %d, want 1", len(s3.deletePrefixCalls))
	}
	if got, want := s3.deletePrefixCalls[0], "dvr/tenant-a/stream-x/dvr-1"; got != want {
		t.Errorf("prefix = %q, want %q", got, want)
	}
}

func TestCleaner_LocalVODUsesS3Key(t *testing.T) {
	s3 := &fakeS3{}
	c := &Cleaner{LocalCluster: "eu-west", S3: s3}

	err := c.Delete(context.Background(), ArtifactRef{
		Hash:     "vod-1",
		Type:     "vod",
		TenantID: "tenant-a",
		VODS3Key: "vod/tenant-a/vod-1/movie.mp4",
	})
	if err != nil {
		t.Fatalf("Delete err = %v", err)
	}
	if len(s3.deleteCalls) != 1 {
		t.Fatalf("Delete calls = %d, want 1", len(s3.deleteCalls))
	}
	if got, want := s3.deleteCalls[0], "vod/tenant-a/vod-1/movie.mp4"; got != want {
		t.Errorf("key = %q, want %q", got, want)
	}
}

func TestCleaner_VODFallsBackToS3URL(t *testing.T) {
	// vod_metadata.s3_key absent, but foghorn.artifacts.s3_url is set
	// (legacy / non-upload paths). We must derive the key from the URL
	// and clean the bytes; never silently soft-delete + drop.
	s3 := &fakeS3{}
	c := &Cleaner{LocalCluster: "eu-west", S3: s3}

	err := c.Delete(context.Background(), ArtifactRef{
		Hash:     "vod-1",
		Type:     "vod",
		TenantID: "tenant-a",
		S3URL:    "s3://bucket/vod/tenant-a/vod-1/movie.mp4",
	})
	if err != nil {
		t.Fatalf("Delete err = %v", err)
	}
	if len(s3.deleteCalls) != 1 || s3.deleteCalls[0] != "vod/tenant-a/vod-1/movie.mp4" {
		t.Errorf("deleteCalls = %v", s3.deleteCalls)
	}
}

func TestCleaner_VODFallsBackToBuildKeyFromFormat(t *testing.T) {
	// Federated VOD freezes write to the deterministic shape
	// vod/<tenant>/<hash>/<hash>.<format>; honor it as a last resort
	// when neither s3_key nor s3_url were recorded.
	s3 := &fakeS3{}
	c := &Cleaner{LocalCluster: "eu-west", S3: s3}

	err := c.Delete(context.Background(), ArtifactRef{
		Hash:     "vod-1",
		Type:     "vod",
		TenantID: "tenant-a",
		Format:   "mp4",
	})
	if err != nil {
		t.Fatalf("Delete err = %v", err)
	}
	if len(s3.deleteCalls) != 1 || s3.deleteCalls[0] != "vod/tenant-a/vod-1/vod-1.mp4" {
		t.Errorf("deleteCalls = %v", s3.deleteCalls)
	}
}

func TestCleaner_RemoteOnlyDeploymentNoLocalS3(t *testing.T) {
	// Storage-via-federation: this Foghorn has no local S3, but the
	// delegate is wired. Remote rows must still get cleaned.
	called := false
	delegate := func(_ context.Context, _ string, req *pb.DeleteStorageObjectsRequest) (*pb.DeleteStorageObjectsResponse, error) {
		called = true
		if req.GetS3Key() != "vod/tenant-a/vod-1/movie.mp4" {
			t.Errorf("delegate received key = %q", req.GetS3Key())
		}
		return &pb.DeleteStorageObjectsResponse{Accepted: true}, nil
	}
	c := &Cleaner{LocalCluster: "eu-west", S3: nil, Delegate: delegate}

	err := c.Delete(context.Background(), ArtifactRef{
		Hash:             "vod-1",
		Type:             "vod",
		TenantID:         "tenant-a",
		VODS3Key:         "vod/tenant-a/vod-1/movie.mp4",
		StorageClusterID: "us-east",
	})
	if err != nil {
		t.Fatalf("Delete err = %v", err)
	}
	if !called {
		t.Errorf("delegate not called for remote-only deployment")
	}
}

func TestCleaner_LocalDeleteWithNilS3ReturnsTypedErr(t *testing.T) {
	// Storage-via-federation deployment receives a request whose row
	// resolves to "local" (no storage_cluster_id, no origin_cluster_id).
	// We can't free anything without local S3; surface a typed error so
	// the purge job keeps the row and the gRPC handler reports cleanup
	// pending.
	c := &Cleaner{LocalCluster: "eu-west", S3: nil}
	err := c.Delete(context.Background(), ArtifactRef{
		Hash:     "vod-1",
		Type:     "vod",
		TenantID: "tenant-a",
		VODS3Key: "vod/tenant-a/vod-1/movie.mp4",
	})
	if !errors.Is(err, ErrLocalS3Missing) {
		t.Fatalf("err = %v, want ErrLocalS3Missing", err)
	}
}

func TestCleaner_MissingFieldsReturnTypedError(t *testing.T) {
	s3 := &fakeS3{}
	c := &Cleaner{LocalCluster: "eu-west", S3: s3}

	cases := []struct {
		name string
		ref  ArtifactRef
	}{
		{"clip without format", ArtifactRef{Hash: "h", Type: "clip", TenantID: "t", StreamInternal: "s"}},
		{"clip without stream", ArtifactRef{Hash: "h", Type: "clip", TenantID: "t", Format: "mp4"}},
		{"dvr without stream", ArtifactRef{Hash: "h", Type: "dvr", TenantID: "t"}},
		{"vod without s3_key", ArtifactRef{Hash: "h", Type: "vod", TenantID: "t"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := c.Delete(context.Background(), tc.ref)
			if !errors.Is(err, ErrMissingTarget) {
				t.Fatalf("err = %v, want ErrMissingTarget", err)
			}
		})
	}
	if len(s3.deleteCalls)+len(s3.deletePrefixCalls) != 0 {
		t.Errorf("S3 should not be called when target is missing")
	}
}

func TestCleaner_UnsupportedTypeReturnsTypedError(t *testing.T) {
	c := &Cleaner{LocalCluster: "eu-west", S3: &fakeS3{}}
	err := c.Delete(context.Background(), ArtifactRef{Hash: "h", Type: "thumbnail", TenantID: "t"})
	if !errors.Is(err, ErrUnsupportedType) {
		t.Fatalf("err = %v, want ErrUnsupportedType", err)
	}
}

func TestCleaner_RemoteUsesDelegateNotLocalS3(t *testing.T) {
	s3 := &fakeS3{}
	var got *pb.DeleteStorageObjectsRequest
	delegate := func(_ context.Context, target string, req *pb.DeleteStorageObjectsRequest) (*pb.DeleteStorageObjectsResponse, error) {
		got = req
		if target != "us-east" {
			t.Errorf("delegate target = %q, want us-east", target)
		}
		return &pb.DeleteStorageObjectsResponse{Accepted: true}, nil
	}
	c := &Cleaner{LocalCluster: "eu-west", S3: s3, Delegate: delegate}

	err := c.Delete(context.Background(), ArtifactRef{
		Hash:             "vod-2",
		Type:             "vod",
		TenantID:         "tenant-a",
		VODS3Key:         "vod/tenant-a/vod-2/movie.mp4",
		StorageClusterID: "us-east",
	})
	if err != nil {
		t.Fatalf("Delete err = %v", err)
	}
	if got == nil {
		t.Fatalf("delegate not called")
	}
	if got.GetTargetClusterId() != "us-east" {
		t.Errorf("TargetClusterId = %q", got.GetTargetClusterId())
	}
	if got.GetRequestingCluster() != "eu-west" {
		t.Errorf("RequestingCluster = %q", got.GetRequestingCluster())
	}
	if got.GetS3Key() != "vod/tenant-a/vod-2/movie.mp4" {
		t.Errorf("S3Key = %q", got.GetS3Key())
	}
	if len(s3.deleteCalls)+len(s3.deletePrefixCalls) != 0 {
		t.Errorf("local S3 must not be called for remote storage")
	}
}

func TestCleaner_RemoteDVRSendsPrefix(t *testing.T) {
	var got *pb.DeleteStorageObjectsRequest
	delegate := func(_ context.Context, _ string, req *pb.DeleteStorageObjectsRequest) (*pb.DeleteStorageObjectsResponse, error) {
		got = req
		return &pb.DeleteStorageObjectsResponse{Accepted: true}, nil
	}
	c := &Cleaner{LocalCluster: "eu-west", S3: &fakeS3{}, Delegate: delegate}

	err := c.Delete(context.Background(), ArtifactRef{
		Hash:             "dvr-2",
		Type:             "dvr",
		TenantID:         "tenant-a",
		StreamInternal:   "stream-x",
		StorageClusterID: "us-east",
	})
	if err != nil {
		t.Fatalf("Delete err = %v", err)
	}
	if got.GetS3Prefix() != "dvr/tenant-a/stream-x/dvr-2" {
		t.Errorf("S3Prefix = %q", got.GetS3Prefix())
	}
	if got.GetS3Key() != "" {
		t.Errorf("S3Key should be empty for dvr, got %q", got.GetS3Key())
	}
}

func TestCleaner_RemoteWithoutDelegateReturnsErr(t *testing.T) {
	c := &Cleaner{LocalCluster: "eu-west", S3: &fakeS3{}, Delegate: nil}
	err := c.Delete(context.Background(), ArtifactRef{
		Hash:             "vod-3",
		Type:             "vod",
		TenantID:         "tenant-a",
		VODS3Key:         "vod/tenant-a/vod-3/x.mp4",
		StorageClusterID: "us-east",
	})
	if !errors.Is(err, ErrDelegateMissing) {
		t.Fatalf("err = %v, want ErrDelegateMissing", err)
	}
}

func TestCleaner_RemoteRejectionPropagatesReason(t *testing.T) {
	delegate := func(_ context.Context, _ string, _ *pb.DeleteStorageObjectsRequest) (*pb.DeleteStorageObjectsResponse, error) {
		return &pb.DeleteStorageObjectsResponse{Accepted: false, Reason: "tenant_mismatch"}, nil
	}
	c := &Cleaner{LocalCluster: "eu-west", S3: &fakeS3{}, Delegate: delegate}
	err := c.Delete(context.Background(), ArtifactRef{
		Hash:             "vod-4",
		Type:             "vod",
		TenantID:         "tenant-a",
		VODS3Key:         "vod/tenant-a/vod-4/x.mp4",
		StorageClusterID: "us-east",
	})
	if !errors.Is(err, ErrRemoteRejected) {
		t.Fatalf("err = %v, want ErrRemoteRejected", err)
	}
	if !strings.Contains(err.Error(), "tenant_mismatch") {
		t.Errorf("error doesn't carry reason: %v", err)
	}
}

func TestCleaner_OriginClusterFallbackForRemoteCheck(t *testing.T) {
	// storage_cluster_id empty, origin_cluster_id != local → remote
	called := false
	delegate := func(_ context.Context, _ string, _ *pb.DeleteStorageObjectsRequest) (*pb.DeleteStorageObjectsResponse, error) {
		called = true
		return &pb.DeleteStorageObjectsResponse{Accepted: true}, nil
	}
	c := &Cleaner{LocalCluster: "eu-west", S3: &fakeS3{}, Delegate: delegate}

	err := c.Delete(context.Background(), ArtifactRef{
		Hash:            "clip-3",
		Type:            "clip",
		TenantID:        "tenant-a",
		StreamInternal:  "s",
		Format:          "mp4",
		OriginClusterID: "us-east",
	})
	if err != nil {
		t.Fatalf("Delete err = %v", err)
	}
	if !called {
		t.Errorf("delegate not called for origin-cluster fallback")
	}
}

func TestCleaner_LocalClusterMatchUsesLocalS3(t *testing.T) {
	// storage_cluster_id == local → local S3
	s3 := &fakeS3{}
	delegate := func(_ context.Context, _ string, _ *pb.DeleteStorageObjectsRequest) (*pb.DeleteStorageObjectsResponse, error) {
		t.Fatal("delegate should not be called when storage_cluster_id == local")
		return nil, nil
	}
	c := &Cleaner{LocalCluster: "eu-west", S3: s3, Delegate: delegate}

	err := c.Delete(context.Background(), ArtifactRef{
		Hash:             "clip-4",
		Type:             "clip",
		TenantID:         "tenant-a",
		StreamInternal:   "s",
		Format:           "mp4",
		StorageClusterID: "eu-west",
	})
	if err != nil {
		t.Fatalf("Delete err = %v", err)
	}
	if len(s3.deleteCalls) != 1 {
		t.Errorf("local Delete calls = %d, want 1", len(s3.deleteCalls))
	}
}

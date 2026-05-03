package federation

import (
	"context"
	"errors"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"frameworks/pkg/logging"
	pb "frameworks/pkg/proto"
)

// fakeDeleteS3Client captures Delete/DeletePrefix calls and lets a test
// inject errors. Only the methods used by DeleteStorageObjects need
// real behavior; everything else satisfies FederationS3Client with
// no-op stubs.
type fakeDeleteS3Client struct {
	fakeMintS3Client
	deleteCalls       []string
	deletePrefixCalls []string
	deleteErr         error
	deletePrefixErr   error
}

func (f *fakeDeleteS3Client) Delete(_ context.Context, key string) error {
	f.deleteCalls = append(f.deleteCalls, key)
	return f.deleteErr
}
func (f *fakeDeleteS3Client) DeletePrefix(_ context.Context, prefix string) (int, error) {
	f.deletePrefixCalls = append(f.deletePrefixCalls, prefix)
	return 0, f.deletePrefixErr
}

func newDeleteServer(t *testing.T, fake *fakeDeleteS3Client) *FederationServer {
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

func TestDeleteStorageObjects_RequiresServiceAuth(t *testing.T) {
	srv := newDeleteServer(t, &fakeDeleteS3Client{})
	_, err := srv.DeleteStorageObjects(context.Background(), &pb.DeleteStorageObjectsRequest{
		TenantId:        "tenant-a",
		TargetClusterId: "platform-eu",
		ArtifactType:    "vod",
		ArtifactHash:    "v1",
		Target:          &pb.DeleteStorageObjectsRequest_S3Key{S3Key: "vod/tenant-a/v1/m.mp4"},
	})
	if status.Code(err) != codes.PermissionDenied {
		t.Fatalf("expected PermissionDenied, got %v", err)
	}
}

func TestDeleteStorageObjects_RejectsTargetWeDoNotOwn(t *testing.T) {
	srv := newDeleteServer(t, &fakeDeleteS3Client{})
	resp, err := srv.DeleteStorageObjects(serviceAuthContext(), &pb.DeleteStorageObjectsRequest{
		TenantId:        "tenant-a",
		TargetClusterId: "selfhost-x",
		ArtifactType:    "vod",
		ArtifactHash:    "v1",
		Target:          &pb.DeleteStorageObjectsRequest_S3Key{S3Key: "vod/tenant-a/v1/m.mp4"},
	})
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if resp.GetAccepted() {
		t.Fatalf("expected accepted=false for unowned cluster")
	}
	if resp.GetReason() != "storage_not_owned_here" {
		t.Errorf("reason = %q", resp.GetReason())
	}
}

func TestDeleteStorageObjects_VODSuccess(t *testing.T) {
	fake := &fakeDeleteS3Client{}
	srv := newDeleteServer(t, fake)
	resp, err := srv.DeleteStorageObjects(serviceAuthContext(), &pb.DeleteStorageObjectsRequest{
		TenantId:        "tenant-a",
		TargetClusterId: "platform-eu",
		ArtifactType:    "vod",
		ArtifactHash:    "v1",
		Target:          &pb.DeleteStorageObjectsRequest_S3Key{S3Key: "vod/tenant-a/v1/m.mp4"},
	})
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if !resp.GetAccepted() {
		t.Fatalf("expected accepted=true, got reason=%q", resp.GetReason())
	}
	if got := fake.deleteCalls; len(got) != 1 || got[0] != "vod/tenant-a/v1/m.mp4" {
		t.Errorf("deleteCalls = %v", got)
	}
}

func TestDeleteStorageObjects_DVRSuccess(t *testing.T) {
	fake := &fakeDeleteS3Client{}
	srv := newDeleteServer(t, fake)
	resp, err := srv.DeleteStorageObjects(serviceAuthContext(), &pb.DeleteStorageObjectsRequest{
		TenantId:        "tenant-a",
		TargetClusterId: "platform-eu",
		ArtifactType:    "dvr",
		ArtifactHash:    "d1",
		Target:          &pb.DeleteStorageObjectsRequest_S3Prefix{S3Prefix: "dvr/tenant-a/stream-x/d1"},
	})
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if !resp.GetAccepted() {
		t.Fatalf("expected accepted=true, got reason=%q", resp.GetReason())
	}
	if got := fake.deletePrefixCalls; len(got) != 1 || got[0] != "dvr/tenant-a/stream-x/d1" {
		t.Errorf("deletePrefixCalls = %v", got)
	}
}

func TestDeleteStorageObjects_InvalidTargetShape(t *testing.T) {
	cases := []struct {
		name string
		req  *pb.DeleteStorageObjectsRequest
	}{
		{
			"clip key in different tenant namespace",
			&pb.DeleteStorageObjectsRequest{
				TenantId:        "tenant-a",
				TargetClusterId: "platform-eu",
				ArtifactType:    "clip",
				ArtifactHash:    "c1",
				Target:          &pb.DeleteStorageObjectsRequest_S3Key{S3Key: "clips/tenant-b/stream/c1.mp4"},
			},
		},
		{
			"vod key not under vod/<tenant>/",
			&pb.DeleteStorageObjectsRequest{
				TenantId:        "tenant-a",
				TargetClusterId: "platform-eu",
				ArtifactType:    "vod",
				ArtifactHash:    "v1",
				Target:          &pb.DeleteStorageObjectsRequest_S3Key{S3Key: "garbage/key"},
			},
		},
		{
			"dvr prefix in different tenant namespace",
			&pb.DeleteStorageObjectsRequest{
				TenantId:        "tenant-a",
				TargetClusterId: "platform-eu",
				ArtifactType:    "dvr",
				ArtifactHash:    "d1",
				Target:          &pb.DeleteStorageObjectsRequest_S3Prefix{S3Prefix: "dvr/tenant-b/stream/d1"},
			},
		},
		{
			"clip key targets a different artifact in same tenant",
			&pb.DeleteStorageObjectsRequest{
				TenantId:        "tenant-a",
				TargetClusterId: "platform-eu",
				ArtifactType:    "clip",
				ArtifactHash:    "c1",
				Target:          &pb.DeleteStorageObjectsRequest_S3Key{S3Key: "clips/tenant-a/stream/c2.mp4"},
			},
		},
		{
			"clip key without extension",
			&pb.DeleteStorageObjectsRequest{
				TenantId:        "tenant-a",
				TargetClusterId: "platform-eu",
				ArtifactType:    "clip",
				ArtifactHash:    "c1",
				Target:          &pb.DeleteStorageObjectsRequest_S3Key{S3Key: "clips/tenant-a/stream/c1"},
			},
		},
		{
			"vod key targets a different artifact in same tenant",
			&pb.DeleteStorageObjectsRequest{
				TenantId:        "tenant-a",
				TargetClusterId: "platform-eu",
				ArtifactType:    "vod",
				ArtifactHash:    "v1",
				Target:          &pb.DeleteStorageObjectsRequest_S3Key{S3Key: "vod/tenant-a/v2/movie.mp4"},
			},
		},
		{
			"vod key without filename component (tenant-wide attempt)",
			&pb.DeleteStorageObjectsRequest{
				TenantId:        "tenant-a",
				TargetClusterId: "platform-eu",
				ArtifactType:    "vod",
				ArtifactHash:    "v1",
				Target:          &pb.DeleteStorageObjectsRequest_S3Key{S3Key: "vod/tenant-a/v1"},
			},
		},
		{
			"dvr prefix targets a different artifact in same tenant",
			&pb.DeleteStorageObjectsRequest{
				TenantId:        "tenant-a",
				TargetClusterId: "platform-eu",
				ArtifactType:    "dvr",
				ArtifactHash:    "d1",
				Target:          &pb.DeleteStorageObjectsRequest_S3Prefix{S3Prefix: "dvr/tenant-a/stream/d2"},
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fake := &fakeDeleteS3Client{}
			srv := newDeleteServer(t, fake)
			resp, err := srv.DeleteStorageObjects(serviceAuthContext(), tc.req)
			if err != nil {
				t.Fatalf("err = %v", err)
			}
			if resp.GetAccepted() {
				t.Fatalf("expected accepted=false")
			}
			if resp.GetReason() != "invalid_target_shape" {
				t.Errorf("reason = %q, want invalid_target_shape", resp.GetReason())
			}
			if len(fake.deleteCalls)+len(fake.deletePrefixCalls) != 0 {
				t.Errorf("S3 must not be touched on shape rejection")
			}
		})
	}
}

func TestDeleteStorageObjects_MissingTarget(t *testing.T) {
	srv := newDeleteServer(t, &fakeDeleteS3Client{})
	resp, err := srv.DeleteStorageObjects(serviceAuthContext(), &pb.DeleteStorageObjectsRequest{
		TenantId:        "tenant-a",
		TargetClusterId: "platform-eu",
		ArtifactType:    "vod",
		ArtifactHash:    "v1",
		// no Target oneof set
	})
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if resp.GetAccepted() {
		t.Fatalf("expected accepted=false")
	}
	if resp.GetReason() != "missing_target" {
		t.Errorf("reason = %q", resp.GetReason())
	}
}

func TestDeleteStorageObjects_TenantMismatchAgainstLocalRow(t *testing.T) {
	fake := &fakeDeleteS3Client{}
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()

	cfg := FederationServerConfig{
		Logger:    logging.NewLogger(),
		ClusterID: "platform-eu",
		DB:        db,
		S3Client:  fake,
		LocalS3Backing: S3Backing{
			Bucket: "frameworks", Endpoint: "https://s3.example.com", Region: "us-east-1",
		},
		IsServedCluster: func(id string) bool { return id == "platform-eu" },
		AdvertisedBacking: func(_ context.Context, _, clusterID string) (S3Backing, bool) {
			if clusterID == "platform-eu" {
				return S3Backing{Bucket: "frameworks", Endpoint: "https://s3.example.com", Region: "us-east-1"}, true
			}
			return S3Backing{}, false
		},
	}
	srv := NewFederationServer(cfg)

	rows := sqlmock.NewRows([]string{"tenant_id"}).AddRow("tenant-other")
	mock.ExpectQuery("FROM foghorn.artifacts WHERE artifact_hash").WillReturnRows(rows)

	resp, err := srv.DeleteStorageObjects(serviceAuthContext(), &pb.DeleteStorageObjectsRequest{
		TenantId:        "tenant-a",
		TargetClusterId: "platform-eu",
		ArtifactType:    "vod",
		ArtifactHash:    "v1",
		Target:          &pb.DeleteStorageObjectsRequest_S3Key{S3Key: "vod/tenant-a/v1/m.mp4"},
	})
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if resp.GetAccepted() {
		t.Fatalf("expected accepted=false")
	}
	if resp.GetReason() != "tenant_mismatch" {
		t.Errorf("reason = %q", resp.GetReason())
	}
	if len(fake.deleteCalls) != 0 {
		t.Errorf("S3 must not be deleted on tenant mismatch")
	}
}

func TestDeleteStorageObjects_S3ErrorPropagates(t *testing.T) {
	fake := &fakeDeleteS3Client{deleteErr: errors.New("503 throttled")}
	srv := newDeleteServer(t, fake)
	resp, err := srv.DeleteStorageObjects(serviceAuthContext(), &pb.DeleteStorageObjectsRequest{
		TenantId:        "tenant-a",
		TargetClusterId: "platform-eu",
		ArtifactType:    "vod",
		ArtifactHash:    "v1",
		Target:          &pb.DeleteStorageObjectsRequest_S3Key{S3Key: "vod/tenant-a/v1/m.mp4"},
	})
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if resp.GetAccepted() {
		t.Fatalf("expected accepted=false on s3 error")
	}
	if resp.GetReason() != "s3_error" {
		t.Errorf("reason = %q", resp.GetReason())
	}
}

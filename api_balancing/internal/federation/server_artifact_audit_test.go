package federation

import (
	"context"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"

	"frameworks/api_balancing/internal/state"
	"frameworks/api_balancing/internal/storage"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	foghornfederationpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/foghorn_federation"
	sharedpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/shared"
)

type clipCreatorSpy struct {
	called bool
}

func (c *clipCreatorSpy) CreateClip(context.Context, *sharedpb.CreateClipRequest) (*sharedpb.CreateClipResponse, error) {
	c.called = true
	return &sharedpb.CreateClipResponse{ClipHash: "cliphash", NodeId: "node-a"}, nil
}

type dvrCreatorSpy struct {
	called bool
}

func (d *dvrCreatorSpy) StartDVR(context.Context, *sharedpb.StartDVRRequest) (*sharedpb.StartDVRResponse, error) {
	d.called = true
	return &sharedpb.StartDVRResponse{DvrHash: "dvrhash"}, nil
}

func TestPrepareArtifactRejectsInconsistentS3Metadata(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer db.Close()

	rows := sqlmock.NewRows([]string{"internal_name", "stream_internal_name", "artifact_type", "format", "storage_location", "sync_status", "size_bytes", "authoritative_cluster"}).
		AddRow("stream-a", "source-stream-a", "clip", "mp4", "s3", "failed", 1024, nil)
	mock.ExpectQuery("FROM foghorn.artifacts").WillReturnRows(rows)

	srv := NewFederationServer(FederationServerConfig{
		Logger:   logging.NewLogger(),
		DB:       db,
		S3Client: &storage.S3Client{},
	})

	resp, err := srv.PrepareArtifact(serviceAuthContext(), &foghornfederationpb.PrepareArtifactRequest{
		ArtifactId: "artifact-1",
		TenantId:   "tenant-a",
	})
	if err != nil {
		t.Fatalf("PrepareArtifact() err = %v", err)
	}
	if resp.GetError() == "" {
		t.Fatalf("expected consistency error, got %+v", resp)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("sql expectations: %v", err)
	}
}

func TestPrepareArtifactRejectsTypeMismatch(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer db.Close()

	rows := sqlmock.NewRows([]string{"internal_name", "stream_internal_name", "artifact_type", "format", "storage_location", "sync_status", "size_bytes", "authoritative_cluster"}).
		AddRow("stream-a", "source-stream-a", "clip", "mp4", "s3", "synced", 1024, nil)
	mock.ExpectQuery("FROM foghorn.artifacts").WillReturnRows(rows)

	srv := NewFederationServer(FederationServerConfig{
		Logger:   logging.NewLogger(),
		DB:       db,
		S3Client: &storage.S3Client{},
	})

	resp, err := srv.PrepareArtifact(serviceAuthContext(), &foghornfederationpb.PrepareArtifactRequest{
		ArtifactId:   "artifact-1",
		TenantId:     "tenant-a",
		ArtifactType: "dvr",
	})
	if err != nil {
		t.Fatalf("PrepareArtifact() err = %v", err)
	}
	if got := resp.GetError(); got != "artifact type mismatch" {
		t.Fatalf("expected artifact type mismatch, got %q", got)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("sql expectations: %v", err)
	}
}

func TestCreateRemoteClipRejectsTenantMismatch(t *testing.T) {
	state.ResetDefaultManagerForTests()
	t.Cleanup(func() { state.ResetDefaultManagerForTests() })

	sm := state.DefaultManager()
	if err := sm.UpdateStreamFromBuffer("stream-a", "stream-a", "node-a", "tenant-origin", "FULL", ""); err != nil {
		t.Fatalf("UpdateStreamFromBuffer: %v", err)
	}

	cc := &clipCreatorSpy{}
	srv := NewFederationServer(FederationServerConfig{Logger: logging.NewLogger(), ClipCreator: cc})
	resp, err := srv.CreateRemoteClip(serviceAuthContext(), &foghornfederationpb.RemoteClipRequest{
		StreamInternalName: "stream-a",
		TenantId:           "tenant-other",
	})
	if err != nil {
		t.Fatalf("CreateRemoteClip() err = %v", err)
	}
	if resp.GetAccepted() {
		t.Fatalf("expected request rejection, got %+v", resp)
	}
	if cc.called {
		t.Fatal("clip creator should not be called on tenant mismatch")
	}
}

func TestCreateRemoteDVRRejectsTenantMismatch(t *testing.T) {
	state.ResetDefaultManagerForTests()
	t.Cleanup(func() { state.ResetDefaultManagerForTests() })

	sm := state.DefaultManager()
	if err := sm.UpdateStreamFromBuffer("stream-a", "stream-a", "node-a", "tenant-origin", "FULL", ""); err != nil {
		t.Fatalf("UpdateStreamFromBuffer: %v", err)
	}

	dc := &dvrCreatorSpy{}
	srv := NewFederationServer(FederationServerConfig{Logger: logging.NewLogger(), DVRCreator: dc})
	resp, err := srv.CreateRemoteDVR(serviceAuthContext(), &foghornfederationpb.RemoteDVRRequest{
		StreamInternalName: "stream-a",
		TenantId:           "tenant-other",
	})
	if err != nil {
		t.Fatalf("CreateRemoteDVR() err = %v", err)
	}
	if resp.GetAccepted() {
		t.Fatalf("expected request rejection, got %+v", resp)
	}
	if dc.called {
		t.Fatal("dvr creator should not be called on tenant mismatch")
	}
}

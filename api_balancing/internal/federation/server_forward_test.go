package federation

import (
	"context"
	"strings"
	"testing"

	"frameworks/pkg/ctxkeys"
	"frameworks/pkg/logging"
	pb "frameworks/pkg/proto"

	"github.com/DATA-DOG/go-sqlmock"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type artifactCommandSpy struct {
	deleteClipCalled bool
	stopDVRCalled    bool
	deleteDVRCalled  bool
	deleteVodCalled  bool
	lastStopReq      *pb.StopDVRRequest
	noForwardSeen    bool
	returnNotFound   bool
	returnErr        error
}

func (s *artifactCommandSpy) DeleteClip(_ context.Context, _ *pb.DeleteClipRequest) (*pb.DeleteClipResponse, error) {
	s.deleteClipCalled = true
	if s.returnNotFound {
		return nil, status.Error(codes.NotFound, "not found")
	}
	if s.returnErr != nil {
		return nil, s.returnErr
	}
	return &pb.DeleteClipResponse{Success: true}, nil
}

func (s *artifactCommandSpy) StopDVR(ctx context.Context, req *pb.StopDVRRequest) (*pb.StopDVRResponse, error) {
	s.stopDVRCalled = true
	s.lastStopReq = req
	if ctx.Value(ctxkeys.KeyNoForward) != nil {
		s.noForwardSeen = true
	}
	if s.returnNotFound {
		return nil, status.Error(codes.NotFound, "not found")
	}
	if s.returnErr != nil {
		return nil, s.returnErr
	}
	return &pb.StopDVRResponse{Success: true}, nil
}

func (s *artifactCommandSpy) DeleteDVR(_ context.Context, _ *pb.DeleteDVRRequest) (*pb.DeleteDVRResponse, error) {
	s.deleteDVRCalled = true
	if s.returnNotFound {
		return nil, status.Error(codes.NotFound, "not found")
	}
	if s.returnErr != nil {
		return nil, s.returnErr
	}
	return &pb.DeleteDVRResponse{Success: true}, nil
}

func (s *artifactCommandSpy) DeleteVodAsset(_ context.Context, _ *pb.DeleteVodAssetRequest) (*pb.DeleteVodAssetResponse, error) {
	s.deleteVodCalled = true
	if s.returnNotFound {
		return nil, status.Error(codes.NotFound, "not found")
	}
	if s.returnErr != nil {
		return nil, s.returnErr
	}
	return &pb.DeleteVodAssetResponse{Success: true}, nil
}

func TestForwardArtifactCommand_RequiresAuth(t *testing.T) {
	srv := NewFederationServer(FederationServerConfig{Logger: logging.NewLogger()})
	_, err := srv.ForwardArtifactCommand(context.Background(), &pb.ForwardArtifactCommandRequest{
		Command:      "delete_clip",
		ArtifactHash: "hash-1",
	})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if status.Code(err) != codes.PermissionDenied {
		t.Fatalf("expected PermissionDenied, got %v", status.Code(err))
	}
}

func TestForwardArtifactCommand_RequiresArtifactHash(t *testing.T) {
	srv := NewFederationServer(FederationServerConfig{Logger: logging.NewLogger()})
	_, err := srv.ForwardArtifactCommand(serviceAuthContext(), &pb.ForwardArtifactCommandRequest{
		Command: "delete_clip",
	})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if status.Code(err) != codes.InvalidArgument {
		t.Fatalf("expected InvalidArgument, got %v", status.Code(err))
	}
}

func TestForwardArtifactCommand_RequiresCommand(t *testing.T) {
	srv := NewFederationServer(FederationServerConfig{Logger: logging.NewLogger()})
	_, err := srv.ForwardArtifactCommand(serviceAuthContext(), &pb.ForwardArtifactCommandRequest{
		ArtifactHash: "hash-1",
	})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if status.Code(err) != codes.InvalidArgument {
		t.Fatalf("expected InvalidArgument, got %v", status.Code(err))
	}
}

func TestForwardArtifactCommand_RequiresTenant(t *testing.T) {
	srv := NewFederationServer(FederationServerConfig{Logger: logging.NewLogger()})
	_, err := srv.ForwardArtifactCommand(serviceAuthContext(), &pb.ForwardArtifactCommandRequest{
		Command:      "delete_clip",
		ArtifactHash: "hash-1",
	})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if status.Code(err) != codes.InvalidArgument {
		t.Fatalf("expected InvalidArgument, got %v", status.Code(err))
	}
}

func TestForwardArtifactCommand_DeleteClip_Handled(t *testing.T) {
	spy := &artifactCommandSpy{}
	srv := NewFederationServer(FederationServerConfig{
		Logger:          logging.NewLogger(),
		ArtifactHandler: spy,
	})
	resp, err := srv.ForwardArtifactCommand(serviceAuthContext(), &pb.ForwardArtifactCommandRequest{
		Command:      "delete_clip",
		ArtifactHash: "clip-hash-1",
		TenantId:     "tenant-a",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !resp.GetHandled() {
		t.Fatal("expected handled=true")
	}
	if !spy.deleteClipCalled {
		t.Fatal("DeleteClip should have been called")
	}
}

func TestForwardArtifactCommand_StopDVR_Handled(t *testing.T) {
	spy := &artifactCommandSpy{}
	srv := NewFederationServer(FederationServerConfig{
		Logger:          logging.NewLogger(),
		ArtifactHandler: spy,
	})
	resp, err := srv.ForwardArtifactCommand(serviceAuthContext(), &pb.ForwardArtifactCommandRequest{
		Command:      "stop_dvr",
		ArtifactHash: "dvr-hash-1",
		TenantId:     "tenant-a",
		StreamId:     "stream-1",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !resp.GetHandled() {
		t.Fatal("expected handled=true")
	}
	if !spy.stopDVRCalled {
		t.Fatal("StopDVR should have been called")
	}
}

func TestForwardArtifactCommand_DeleteDVR_Handled(t *testing.T) {
	spy := &artifactCommandSpy{}
	srv := NewFederationServer(FederationServerConfig{
		Logger:          logging.NewLogger(),
		ArtifactHandler: spy,
	})
	resp, err := srv.ForwardArtifactCommand(serviceAuthContext(), &pb.ForwardArtifactCommandRequest{
		Command:      "delete_dvr",
		ArtifactHash: "dvr-hash-1",
		TenantId:     "tenant-a",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !resp.GetHandled() {
		t.Fatal("expected handled=true")
	}
	if !spy.deleteDVRCalled {
		t.Fatal("DeleteDVR should have been called")
	}
}

func TestForwardArtifactCommand_DeleteVod_Handled(t *testing.T) {
	spy := &artifactCommandSpy{}
	srv := NewFederationServer(FederationServerConfig{
		Logger:          logging.NewLogger(),
		ArtifactHandler: spy,
	})
	resp, err := srv.ForwardArtifactCommand(serviceAuthContext(), &pb.ForwardArtifactCommandRequest{
		Command:      "delete_vod",
		ArtifactHash: "vod-hash-1",
		TenantId:     "tenant-a",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !resp.GetHandled() {
		t.Fatal("expected handled=true")
	}
	if !spy.deleteVodCalled {
		t.Fatal("DeleteVodAsset should have been called")
	}
}

func TestForwardArtifactCommand_NotFound_ReturnsFalse(t *testing.T) {
	spy := &artifactCommandSpy{returnNotFound: true}
	srv := NewFederationServer(FederationServerConfig{
		Logger:          logging.NewLogger(),
		ArtifactHandler: spy,
	})
	resp, err := srv.ForwardArtifactCommand(serviceAuthContext(), &pb.ForwardArtifactCommandRequest{
		Command:      "delete_clip",
		ArtifactHash: "clip-hash-missing",
		TenantId:     "tenant-a",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.GetHandled() {
		t.Fatal("expected handled=false when artifact not found")
	}
}

func TestForwardArtifactCommand_UnknownCommand(t *testing.T) {
	spy := &artifactCommandSpy{}
	srv := NewFederationServer(FederationServerConfig{
		Logger:          logging.NewLogger(),
		ArtifactHandler: spy,
	})
	resp, err := srv.ForwardArtifactCommand(serviceAuthContext(), &pb.ForwardArtifactCommandRequest{
		Command:      "nuke_everything",
		ArtifactHash: "hash-1",
		TenantId:     "tenant-a",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.GetHandled() {
		t.Fatal("expected handled=false for unknown command")
	}
	if resp.GetError() == "" {
		t.Fatal("expected error message for unknown command")
	}
}

func TestForwardArtifactCommand_NilHandler(t *testing.T) {
	srv := NewFederationServer(FederationServerConfig{
		Logger: logging.NewLogger(),
		// No ArtifactHandler wired
	})
	resp, err := srv.ForwardArtifactCommand(serviceAuthContext(), &pb.ForwardArtifactCommandRequest{
		Command:      "delete_clip",
		ArtifactHash: "hash-1",
		TenantId:     "tenant-a",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.GetHandled() {
		t.Fatal("expected handled=false when handler is nil")
	}
	if resp.GetError() == "" {
		t.Fatal("expected error message when handler is nil")
	}
}

func TestForwardArtifactCommand_StopDVR_StreamIDNotSetWhenEmpty(t *testing.T) {
	spy := &artifactCommandSpy{}
	srv := NewFederationServer(FederationServerConfig{
		Logger:          logging.NewLogger(),
		ArtifactHandler: spy,
	})

	resp, err := srv.ForwardArtifactCommand(serviceAuthContext(), &pb.ForwardArtifactCommandRequest{
		Command:      "stop_dvr",
		ArtifactHash: "dvr-hash-1",
		TenantId:     "tenant-a",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !resp.GetHandled() {
		t.Fatal("expected handled=true")
	}
	if spy.lastStopReq == nil {
		t.Fatal("expected stop request to be captured")
	}
	if spy.lastStopReq.StreamId != nil {
		t.Fatal("expected StreamId to be nil when forwarded stream_id is empty")
	}
	if !spy.noForwardSeen {
		t.Fatal("expected no-forward context guard to be set")
	}
}

func TestForwardArtifactCommand_StopDVR_StreamIDMismatchRejected(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer db.Close()

	rows := sqlmock.NewRows([]string{"stream_id"}).AddRow("stream-db")
	mock.ExpectQuery("SELECT stream_id::text FROM foghorn.artifacts").
		WithArgs("dvr-hash-1", "dvr", "tenant-a").
		WillReturnRows(rows)

	spy := &artifactCommandSpy{}
	srv := NewFederationServer(FederationServerConfig{
		Logger:          logging.NewLogger(),
		DB:              db,
		ArtifactHandler: spy,
	})

	resp, err := srv.ForwardArtifactCommand(serviceAuthContext(), &pb.ForwardArtifactCommandRequest{
		Command:      "stop_dvr",
		ArtifactHash: "dvr-hash-1",
		TenantId:     "tenant-a",
		StreamId:     "stream-client",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.GetHandled() {
		t.Fatal("expected handled=false when stream_id mismatches local artifact")
	}
	if !strings.Contains(resp.GetError(), "stream_id mismatch") {
		t.Fatalf("expected stream_id mismatch error, got %q", resp.GetError())
	}
	if spy.stopDVRCalled {
		t.Fatal("handler must not run when revalidation fails")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("sql expectations not met: %v", err)
	}
}

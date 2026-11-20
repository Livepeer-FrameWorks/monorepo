package grpc

import (
	"testing"

	sharedpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/shared"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestCreateVodUploadRefusesUnsupportedFilenameBeforeRegistering(t *testing.T) {
	s, mock, cleanup := newMockServer(t)
	defer cleanup()
	_, err := s.CreateVodUpload(ctxAs("user", "tenant", "owner"), &sharedpb.CreateVodUploadRequest{Filename: "playlist.m3u8", SizeBytes: 1024})
	if status.Code(err) != codes.InvalidArgument {
		t.Fatalf("error = %v, want InvalidArgument before any database access", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

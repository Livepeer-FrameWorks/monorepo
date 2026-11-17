package triggers

import (
	"context"
	"errors"
	"testing"

	"frameworks/api_balancing/internal/federation"
	localauthority "frameworks/api_balancing/internal/mediaauthority"
	ipcpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/ipc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestIngestTimeoutRequiresAnActualDeadline(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
		want ipcpb.IngestErrorCode
	}{
		{"local deadline", context.DeadlineExceeded, ipcpb.IngestErrorCode_INGEST_ERROR_TIMEOUT},
		{"remote deadline", status.Error(codes.DeadlineExceeded, "expired"), ipcpb.IngestErrorCode_INGEST_ERROR_TIMEOUT},
		{"fetch deadline", errors.Join(localauthority.ErrAuthorityFetchFailed, context.DeadlineExceeded), ipcpb.IngestErrorCode_INGEST_ERROR_TIMEOUT},
		{"unavailable", status.Error(codes.Unavailable, "disconnected"), ipcpb.IngestErrorCode_INGEST_ERROR_INTERNAL},
		{"changed authority", federation.ErrPlacementAuthorityChanged, ipcpb.IngestErrorCode_INGEST_ERROR_INTERNAL},
		{"aborted", status.Error(codes.Aborted, "changed"), ipcpb.IngestErrorCode_INGEST_ERROR_INTERNAL},
		{"fetch failure", localauthority.ErrAuthorityFetchFailed, ipcpb.IngestErrorCode_INGEST_ERROR_INTERNAL},
		{"canceled", context.Canceled, ipcpb.IngestErrorCode_INGEST_ERROR_INTERNAL},
		{"policy", status.Error(codes.PermissionDenied, "denied"), ipcpb.IngestErrorCode_INGEST_ERROR_PLACEMENT_DENIED},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := ingestPlacementErrorCode(tc.err); got != tc.want {
				t.Fatalf("error %v classified as %s, want %s", tc.err, got, tc.want)
			}
		})
	}
}

package grpc

import (
	"context"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	foghornpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/foghorn"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// OverrideArtifactRetention validates its inputs, resolves the horizon, then
// updates only a finalized matching row. It rejects empty ids, unsupported
// types, and (via affected==0) an active/absent artifact; the happy path stamps
// retention_until and reports Applied.
func TestOverrideArtifactRetention(t *testing.T) {
	until := timestamppb.New(time.Unix(1800000000, 0))

	t.Run("missing tenant id", func(t *testing.T) {
		s, _, done := newRetentionServer(t)
		defer done()
		_, err := s.OverrideArtifactRetention(context.Background(), &foghornpb.OverrideArtifactRetentionRequest{DvrHash: "h", RetentionUntil: until})
		if status.Code(err) != codes.InvalidArgument {
			t.Fatalf("want InvalidArgument, got %v", err)
		}
	})

	t.Run("missing artifact hash", func(t *testing.T) {
		s, _, done := newRetentionServer(t)
		defer done()
		_, err := s.OverrideArtifactRetention(context.Background(), &foghornpb.OverrideArtifactRetentionRequest{TenantId: "t", RetentionUntil: until})
		if status.Code(err) != codes.InvalidArgument {
			t.Fatalf("want InvalidArgument, got %v", err)
		}
	})

	t.Run("unsupported artifact type", func(t *testing.T) {
		s, _, done := newRetentionServer(t)
		defer done()
		_, err := s.OverrideArtifactRetention(context.Background(), &foghornpb.OverrideArtifactRetentionRequest{
			TenantId: "t", DvrHash: "h", ArtifactType: "weird", RetentionUntil: until,
		})
		if status.Code(err) != codes.InvalidArgument {
			t.Fatalf("want InvalidArgument, got %v", err)
		}
	})

	t.Run("applied to a finalized row", func(t *testing.T) {
		s, mock, done := newRetentionServer(t)
		defer done()
		mock.ExpectExec(`UPDATE foghorn.artifacts\s+SET retention_until = \$1\s+WHERE artifact_hash = \$2\s+AND tenant_id::text = \$3\s+AND artifact_type = \$4\s+AND status IN \('completed', 'completed_partial', 'ready', 'failed'\)`).
			WithArgs(sqlmock.AnyArg(), "h", "t", "dvr").
			WillReturnResult(sqlmock.NewResult(0, 1))
		resp, err := s.OverrideArtifactRetention(context.Background(), &foghornpb.OverrideArtifactRetentionRequest{
			TenantId: "t", DvrHash: "h", RetentionUntil: until, // default type dvr
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !resp.GetApplied() {
			t.Fatal("expected Applied=true")
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("active or absent artifact is rejected", func(t *testing.T) {
		s, mock, done := newRetentionServer(t)
		defer done()
		mock.ExpectExec(`UPDATE foghorn.artifacts\s+SET retention_until`).
			WithArgs(sqlmock.AnyArg(), "h", "t", "clip").
			WillReturnResult(sqlmock.NewResult(0, 0)) // no finalized row matched
		_, err := s.OverrideArtifactRetention(context.Background(), &foghornpb.OverrideArtifactRetentionRequest{
			TenantId: "t", DvrHash: "h", ArtifactType: "clip", RetentionUntil: until,
		})
		if status.Code(err) != codes.FailedPrecondition {
			t.Fatalf("want FailedPrecondition, got %v", err)
		}
	})

	t.Run("no horizon specified is invalid", func(t *testing.T) {
		s, _, done := newRetentionServer(t)
		defer done()
		// Neither retention_until nor anchor_to_ended_at → resolveRetentionUntil errors.
		_, err := s.OverrideArtifactRetention(context.Background(), &foghornpb.OverrideArtifactRetentionRequest{
			TenantId: "t", DvrHash: "h",
		})
		if status.Code(err) != codes.InvalidArgument {
			t.Fatalf("want InvalidArgument, got %v", err)
		}
	})
}

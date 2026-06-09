package grpc

import (
	"context"
	"database/sql"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	commodorepb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/commodore"
	"google.golang.org/grpc/codes"
)

// expectUniqueArtifactIdentifiers mirrors generateUniqueArtifactIdentifiers:
// it probes identifierExists twice (internal name, then playback id) before
// committing. On the happy path both probes return false (the freshly
// generated identifiers are unused).
func expectUniqueArtifactIdentifiers(mock sqlmock.Sqlmock) {
	mock.ExpectQuery("SELECT EXISTS").
		WithArgs(sqlmock.AnyArg()).
		WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(false))
	mock.ExpectQuery("SELECT EXISTS").
		WithArgs(sqlmock.AnyArg()).
		WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(false))
}

func TestRegisterClip(t *testing.T) {
	t.Run("missing_required_fields", func(t *testing.T) {
		s, _, done := newMockServer(t)
		defer done()
		_, err := s.RegisterClip(context.Background(), &commodorepb.RegisterClipRequest{TenantId: "t1", UserId: "u1"})
		wantCode(t, err, codes.InvalidArgument)
	})

	t.Run("stream_not_found", func(t *testing.T) {
		s, mock, done := newMockServer(t)
		defer done()
		expectUniqueArtifactIdentifiers(mock)
		mock.ExpectQuery("FROM commodore.streams").
			WithArgs("s1", "t1").
			WillReturnError(sql.ErrNoRows)
		_, err := s.RegisterClip(context.Background(), &commodorepb.RegisterClipRequest{
			TenantId: "t1", UserId: "u1", StreamId: "s1",
		})
		wantCode(t, err, codes.NotFound)
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Fatalf("expectations: %v", err)
		}
	})

	t.Run("happy_path_inherits_source_auth_and_emits", func(t *testing.T) {
		s, mock, done := newMockServer(t)
		defer done()
		expectUniqueArtifactIdentifiers(mock)
		// Clip inherits the source stream's auth/policy so a private stream's
		// clips stay private.
		mock.ExpectQuery("FROM commodore.streams").
			WithArgs("s1", "t1").
			WillReturnRows(sqlmock.NewRows([]string{"requires_auth", "playback_policy", "playback_webhook_secret_enc"}).
				AddRow(true, `{"type":"jwt"}`, nil))
		mock.ExpectExec("INSERT INTO commodore.clips").
			WillReturnResult(sqlmock.NewResult(0, 1))
		expectOutboxInsert(mock)

		resp, err := s.RegisterClip(context.Background(), &commodorepb.RegisterClipRequest{
			TenantId: "t1", UserId: "u1", StreamId: "s1", Title: "clip",
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if resp.GetClipHash() == "" || resp.GetClipId() == "" {
			t.Errorf("expected generated clip hash and id, got %+v", resp)
		}
		if resp.GetPlaybackId() == "" || resp.GetInternalName() == "" {
			t.Errorf("expected generated artifact identifiers, got %+v", resp)
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Fatalf("expectations: %v", err)
		}
	})
}

func TestRegisterDVR(t *testing.T) {
	t.Run("missing_required_fields", func(t *testing.T) {
		s, _, done := newMockServer(t)
		defer done()
		// stream_internal_name (not playback_id) is the required source key.
		_, err := s.RegisterDVR(context.Background(), &commodorepb.RegisterDVRRequest{TenantId: "t1", UserId: "u1"})
		wantCode(t, err, codes.InvalidArgument)
	})

	t.Run("stream_not_found_by_internal_name", func(t *testing.T) {
		s, mock, done := newMockServer(t)
		defer done()
		expectUniqueArtifactIdentifiers(mock)
		// The source lookup must be keyed on internal_name, never playback_id —
		// this WithArgs pins that the request's stream_internal_name is what
		// reaches the WHERE clause.
		mock.ExpectQuery("WHERE internal_name = \\$1 AND tenant_id = \\$2").
			WithArgs("live+stream1", "t1").
			WillReturnError(sql.ErrNoRows)
		_, err := s.RegisterDVR(context.Background(), &commodorepb.RegisterDVRRequest{
			TenantId: "t1", UserId: "u1", StreamInternalName: "live+stream1",
		})
		wantCode(t, err, codes.NotFound)
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Fatalf("expectations: %v", err)
		}
	})

	t.Run("happy_path_resolves_stream_id_and_emits", func(t *testing.T) {
		s, mock, done := newMockServer(t)
		defer done()
		expectUniqueArtifactIdentifiers(mock)
		mock.ExpectQuery("FROM commodore.streams WHERE internal_name").
			WithArgs("live+stream1", "t1").
			WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow("stream-uuid-1"))
		mock.ExpectExec("INSERT INTO commodore.dvr_recordings").
			WillReturnResult(sqlmock.NewResult(0, 1))
		expectOutboxInsert(mock)

		resp, err := s.RegisterDVR(context.Background(), &commodorepb.RegisterDVRRequest{
			TenantId: "t1", UserId: "u1", StreamInternalName: "live+stream1",
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		// The resolved stream UUID — not the internal name — is returned.
		if resp.GetStreamId() != "stream-uuid-1" {
			t.Errorf("stream_id = %q, want stream-uuid-1", resp.GetStreamId())
		}
		if resp.GetDvrHash() == "" || resp.GetDvrId() == "" {
			t.Errorf("expected generated DVR hash and id, got %+v", resp)
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Fatalf("expectations: %v", err)
		}
	})
}

package grpc

import (
	"context"
	"database/sql"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	commodorepb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/commodore"
	"github.com/sirupsen/logrus"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func expectPushStream(mock sqlmock.Sqlmock) {
	mock.ExpectQuery("SELECT ingest_mode").
		WithArgs("s1", "u1", testTenantID).
		WillReturnRows(sqlmock.NewRows([]string{"ingest_mode"}).AddRow("push"))
}

func expectMissingStream(mock sqlmock.Sqlmock) {
	mock.ExpectQuery("SELECT ingest_mode").
		WithArgs("s1", "u1", testTenantID).
		WillReturnError(sql.ErrNoRows)
}

// newMockServer builds a CommodoreServer wired only to a sqlmock DB.
// The nilable cross-service clients (purser/foghorn/quartermaster/...) stay nil
// because every path exercised here returns before touching them.
func newMockServer(t *testing.T) (*CommodoreServer, sqlmock.Sqlmock, func()) {
	t.Helper()
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	server := &CommodoreServer{db: db, logger: logrus.New(), tokenHasher: testTokenHasher(t), accountEmailKickFn: func() {}}
	return server, mock, func() { _ = db.Close() }
}

func wantCode(t *testing.T, err error, want codes.Code) {
	t.Helper()
	if status.Code(err) != want {
		t.Fatalf("status code = %v, want %v (err: %v)", status.Code(err), want, err)
	}
}

func TestRefreshStreamKey(t *testing.T) {
	t.Run("unauthenticated", func(t *testing.T) {
		s, _, done := newMockServer(t)
		defer done()
		_, err := s.RefreshStreamKey(context.Background(), &commodorepb.RefreshStreamKeyRequest{StreamId: "s1"})
		wantCode(t, err, codes.Unauthenticated)
	})

	t.Run("empty_stream_id", func(t *testing.T) {
		s, _, done := newMockServer(t)
		defer done()
		_, err := s.RefreshStreamKey(ctxAs("u1", testTenantID, "owner"), &commodorepb.RefreshStreamKeyRequest{})
		wantCode(t, err, codes.InvalidArgument)
	})

	t.Run("not_found_when_no_rows_updated", func(t *testing.T) {
		s, mock, done := newMockServer(t)
		defer done()
		expectPushStream(mock)
		mock.ExpectBegin()
		mock.ExpectExec("UPDATE commodore.streams").
			WithArgs(sqlmock.AnyArg(), "s1", "u1", testTenantID).
			WillReturnResult(sqlmock.NewResult(0, 0))
		mock.ExpectRollback()
		_, err := s.RefreshStreamKey(ctxAs("u1", testTenantID, "owner"), &commodorepb.RefreshStreamKeyRequest{StreamId: "s1"})
		wantCode(t, err, codes.NotFound)
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Fatalf("expectations: %v", err)
		}
	})

	t.Run("happy_path_rotates_key_and_emits", func(t *testing.T) {
		s, mock, done := newMockServer(t)
		defer done()
		expectPushStream(mock)
		mock.ExpectBegin()
		mock.ExpectExec("UPDATE commodore.streams").
			WithArgs(sqlmock.AnyArg(), "s1", "u1", testTenantID).
			WillReturnResult(sqlmock.NewResult(0, 1))
		expectDualEventInsert(mock, "stream.key_rotated", eventStreamUpdated)
		mock.ExpectCommit()
		mock.ExpectQuery("SELECT playback_id FROM commodore.streams").
			WithArgs("s1", "u1", testTenantID).
			WillReturnRows(sqlmock.NewRows([]string{"playback_id"}).AddRow("pb-1"))

		resp, err := s.RefreshStreamKey(ctxAs("u1", testTenantID, "owner"), &commodorepb.RefreshStreamKeyRequest{StreamId: "s1"})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if resp.GetPlaybackId() != "pb-1" {
			t.Errorf("playback_id = %q, want pb-1", resp.GetPlaybackId())
		}
		if !resp.GetOldKeyInvalidated() {
			t.Error("expected OldKeyInvalidated=true")
		}
		// The new key must actually be a freshly generated stream key, never
		// echoed input.
		if resp.GetStreamKey() == "" || len(resp.GetStreamKey()) < 4 || resp.GetStreamKey()[:3] != "sk_" {
			t.Errorf("stream key %q is not a generated sk_ token", resp.GetStreamKey())
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Fatalf("expectations: %v", err)
		}
	})
}

func TestLegacyAdditionalKeyMutationsRefuse(t *testing.T) {
	for _, name := range []string{"create", "delete"} {
		t.Run(name, func(t *testing.T) {
			call := func(s *CommodoreServer, ctx context.Context, id string) error {
				if name == "create" {
					_, err := s.CreateStreamKey(ctx, &commodorepb.CreateStreamKeyRequest{StreamId: id})
					return err
				}
				_, err := s.DeactivateStreamKey(ctx, &commodorepb.DeactivateStreamKeyRequest{StreamId: id, KeyId: "legacy"})
				return err
			}
			t.Run("unauthenticated", func(t *testing.T) {
				s, _, done := newMockServer(t)
				defer done()
				wantCode(t, call(s, context.Background(), "s1"), codes.Unauthenticated)
			})
			t.Run("missing_id", func(t *testing.T) {
				s, _, done := newMockServer(t)
				defer done()
				wantCode(t, call(s, ctxAs("u1", testTenantID, "owner"), ""), codes.InvalidArgument)
			})
			t.Run("not_owner", func(t *testing.T) {
				s, mock, done := newMockServer(t)
				defer done()
				expectMissingStream(mock)
				wantCode(t, call(s, ctxAs("u1", testTenantID, "owner"), "s1"), codes.NotFound)
				if err := mock.ExpectationsWereMet(); err != nil {
					t.Fatal(err)
				}
			})
			t.Run("rotate_instead", func(t *testing.T) {
				s, mock, done := newMockServer(t)
				defer done()
				expectPushStream(mock)
				wantCode(t, call(s, ctxAs("u1", testTenantID, "owner"), "s1"), codes.FailedPrecondition)
				if err := mock.ExpectationsWereMet(); err != nil {
					t.Fatal(err)
				}
			})
		})
	}
}

func TestListStreamKeys(t *testing.T) {
	t.Run("unauthenticated", func(t *testing.T) {
		s, _, done := newMockServer(t)
		defer done()
		_, err := s.ListStreamKeys(context.Background(), &commodorepb.ListStreamKeysRequest{StreamId: "s1"})
		wantCode(t, err, codes.Unauthenticated)
	})
	t.Run("missing_id", func(t *testing.T) {
		s, _, done := newMockServer(t)
		defer done()
		_, err := s.ListStreamKeys(ctxAs("u1", testTenantID, "owner"), &commodorepb.ListStreamKeysRequest{})
		wantCode(t, err, codes.InvalidArgument)
	})
	t.Run("not_owner", func(t *testing.T) {
		s, mock, done := newMockServer(t)
		defer done()
		expectMissingStream(mock)
		_, err := s.ListStreamKeys(ctxAs("u1", testTenantID, "owner"), &commodorepb.ListStreamKeysRequest{StreamId: "s1"})
		wantCode(t, err, codes.NotFound)
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Fatal(err)
		}
	})
}

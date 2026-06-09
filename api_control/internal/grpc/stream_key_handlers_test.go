package grpc

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	commodorepb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/commodore"
	"github.com/sirupsen/logrus"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// newMockServer builds a CommodoreServer wired only to a sqlmock DB.
// The nilable cross-service clients (purser/foghorn/quartermaster/...) stay nil
// because every path exercised here returns before touching them.
func newMockServer(t *testing.T) (*CommodoreServer, sqlmock.Sqlmock, func()) {
	t.Helper()
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	server := &CommodoreServer{db: db, logger: logrus.New()}
	return server, mock, func() { _ = db.Close() }
}

// expectOutboxInsert mirrors the trailing service-event outbox row that every
// mutating handler writes via emit*Event → EnqueueServiceEventTx. It is a
// QueryRowContext (INSERT ... RETURNING id), so it must be an ExpectQuery.
func expectOutboxInsert(mock sqlmock.Sqlmock) {
	mock.ExpectQuery("INSERT INTO commodore.service_event_outbox").
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow("evt-1"))
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
		_, err := s.RefreshStreamKey(ctxAs("u1", "t1", "owner"), &commodorepb.RefreshStreamKeyRequest{})
		wantCode(t, err, codes.InvalidArgument)
	})

	t.Run("not_found_when_no_rows_updated", func(t *testing.T) {
		s, mock, done := newMockServer(t)
		defer done()
		mock.ExpectExec("UPDATE commodore.streams").
			WithArgs(sqlmock.AnyArg(), "s1", "u1", "t1").
			WillReturnResult(sqlmock.NewResult(0, 0))
		_, err := s.RefreshStreamKey(ctxAs("u1", "t1", "owner"), &commodorepb.RefreshStreamKeyRequest{StreamId: "s1"})
		wantCode(t, err, codes.NotFound)
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Fatalf("expectations: %v", err)
		}
	})

	t.Run("happy_path_rotates_key_and_emits", func(t *testing.T) {
		s, mock, done := newMockServer(t)
		defer done()
		mock.ExpectExec("UPDATE commodore.streams").
			WithArgs(sqlmock.AnyArg(), "s1", "u1", "t1").
			WillReturnResult(sqlmock.NewResult(0, 1))
		mock.ExpectQuery("SELECT playback_id FROM commodore.streams").
			WithArgs("s1").
			WillReturnRows(sqlmock.NewRows([]string{"playback_id"}).AddRow("pb-1"))
		expectOutboxInsert(mock)

		resp, err := s.RefreshStreamKey(ctxAs("u1", "t1", "owner"), &commodorepb.RefreshStreamKeyRequest{StreamId: "s1"})
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

func TestCreateStreamKey(t *testing.T) {
	t.Run("unauthenticated", func(t *testing.T) {
		s, _, done := newMockServer(t)
		defer done()
		_, err := s.CreateStreamKey(context.Background(), &commodorepb.CreateStreamKeyRequest{StreamId: "s1"})
		wantCode(t, err, codes.Unauthenticated)
	})

	t.Run("empty_stream_id", func(t *testing.T) {
		s, _, done := newMockServer(t)
		defer done()
		_, err := s.CreateStreamKey(ctxAs("u1", "t1", "owner"), &commodorepb.CreateStreamKeyRequest{})
		wantCode(t, err, codes.InvalidArgument)
	})

	t.Run("not_found_when_not_owner", func(t *testing.T) {
		s, mock, done := newMockServer(t)
		defer done()
		mock.ExpectQuery("SELECT EXISTS").
			WithArgs("s1", "u1", "t1").
			WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(false))
		_, err := s.CreateStreamKey(ctxAs("u1", "t1", "owner"), &commodorepb.CreateStreamKeyRequest{StreamId: "s1"})
		wantCode(t, err, codes.NotFound)
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Fatalf("expectations: %v", err)
		}
	})

	t.Run("happy_path_inserts_active_key", func(t *testing.T) {
		s, mock, done := newMockServer(t)
		defer done()
		mock.ExpectQuery("SELECT EXISTS").
			WithArgs("s1", "u1", "t1").
			WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(true))
		mock.ExpectExec("INSERT INTO commodore.stream_keys").
			WillReturnResult(sqlmock.NewResult(0, 1))
		expectOutboxInsert(mock)

		resp, err := s.CreateStreamKey(ctxAs("u1", "t1", "owner"), &commodorepb.CreateStreamKeyRequest{StreamId: "s1", KeyName: "primary"})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		key := resp.GetStreamKey()
		if key == nil {
			t.Fatal("nil stream key in response")
		}
		if !key.GetIsActive() {
			t.Error("new key should be active")
		}
		if key.GetKeyName() != "primary" {
			t.Errorf("key name = %q, want primary", key.GetKeyName())
		}
		if key.GetTenantId() != "t1" || key.GetUserId() != "u1" || key.GetStreamId() != "s1" {
			t.Errorf("ownership fields not propagated: %+v", key)
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Fatalf("expectations: %v", err)
		}
	})
}

func TestListStreamKeys(t *testing.T) {
	t.Run("unauthenticated", func(t *testing.T) {
		s, _, done := newMockServer(t)
		defer done()
		_, err := s.ListStreamKeys(context.Background(), &commodorepb.ListStreamKeysRequest{StreamId: "s1"})
		wantCode(t, err, codes.Unauthenticated)
	})

	t.Run("empty_stream_id", func(t *testing.T) {
		s, _, done := newMockServer(t)
		defer done()
		_, err := s.ListStreamKeys(ctxAs("u1", "t1", "owner"), &commodorepb.ListStreamKeysRequest{})
		wantCode(t, err, codes.InvalidArgument)
	})

	t.Run("not_found_when_not_owner", func(t *testing.T) {
		s, mock, done := newMockServer(t)
		defer done()
		mock.ExpectQuery("SELECT EXISTS").
			WithArgs("s1", "u1", "t1").
			WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(false))
		_, err := s.ListStreamKeys(ctxAs("u1", "t1", "owner"), &commodorepb.ListStreamKeysRequest{StreamId: "s1"})
		wantCode(t, err, codes.NotFound)
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Fatalf("expectations: %v", err)
		}
	})

	t.Run("happy_path_projects_keys_and_total", func(t *testing.T) {
		s, mock, done := newMockServer(t)
		defer done()
		now := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
		mock.ExpectQuery("SELECT EXISTS").
			WithArgs("s1", "u1", "t1").
			WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(true))
		mock.ExpectQuery("SELECT COUNT").
			WithArgs("s1").
			WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(2))
		mock.ExpectQuery("FROM commodore.stream_keys").
			WithArgs("s1").
			WillReturnRows(sqlmock.NewRows([]string{
				"id", "tenant_id", "user_id", "stream_id", "key_value", "key_name",
				"is_active", "last_used_at", "created_at", "updated_at",
			}).
				AddRow("k1", "t1", "u1", "s1", "sk_aaa", "first", true, nil, now, now).
				AddRow("k2", "t1", "u1", "s1", "sk_bbb", "second", false, now, now, now))

		resp, err := s.ListStreamKeys(ctxAs("u1", "t1", "owner"), &commodorepb.ListStreamKeysRequest{StreamId: "s1"})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(resp.GetStreamKeys()) != 2 {
			t.Fatalf("keys = %d, want 2", len(resp.GetStreamKeys()))
		}
		if resp.GetPagination().GetTotalCount() != 2 {
			t.Errorf("total = %d, want 2", resp.GetPagination().GetTotalCount())
		}
		// Fewer rows than the page limit means there is no next page.
		if resp.GetPagination().GetHasNextPage() {
			t.Error("expected HasNextPage=false for a single short page")
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Fatalf("expectations: %v", err)
		}
	})
}

func TestDeactivateStreamKey(t *testing.T) {
	t.Run("unauthenticated", func(t *testing.T) {
		s, _, done := newMockServer(t)
		defer done()
		_, err := s.DeactivateStreamKey(context.Background(), &commodorepb.DeactivateStreamKeyRequest{StreamId: "s1", KeyId: "k1"})
		wantCode(t, err, codes.Unauthenticated)
	})

	t.Run("not_found_when_not_owner", func(t *testing.T) {
		s, mock, done := newMockServer(t)
		defer done()
		mock.ExpectQuery("SELECT EXISTS").
			WithArgs("s1", "u1", "t1").
			WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(false))
		_, err := s.DeactivateStreamKey(ctxAs("u1", "t1", "owner"), &commodorepb.DeactivateStreamKeyRequest{StreamId: "s1", KeyId: "k1"})
		wantCode(t, err, codes.NotFound)
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Fatalf("expectations: %v", err)
		}
	})

	t.Run("not_found_when_key_missing", func(t *testing.T) {
		s, mock, done := newMockServer(t)
		defer done()
		mock.ExpectQuery("SELECT EXISTS").
			WithArgs("s1", "u1", "t1").
			WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(true))
		mock.ExpectExec("UPDATE commodore.stream_keys SET is_active = false").
			WithArgs("k1", "s1").
			WillReturnResult(sqlmock.NewResult(0, 0))
		_, err := s.DeactivateStreamKey(ctxAs("u1", "t1", "owner"), &commodorepb.DeactivateStreamKeyRequest{StreamId: "s1", KeyId: "k1"})
		wantCode(t, err, codes.NotFound)
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Fatalf("expectations: %v", err)
		}
	})

	t.Run("happy_path_deactivates_and_emits", func(t *testing.T) {
		s, mock, done := newMockServer(t)
		defer done()
		mock.ExpectQuery("SELECT EXISTS").
			WithArgs("s1", "u1", "t1").
			WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(true))
		mock.ExpectExec("UPDATE commodore.stream_keys SET is_active = false").
			WithArgs("k1", "s1").
			WillReturnResult(sqlmock.NewResult(0, 1))
		expectOutboxInsert(mock)

		_, err := s.DeactivateStreamKey(ctxAs("u1", "t1", "owner"), &commodorepb.DeactivateStreamKeyRequest{StreamId: "s1", KeyId: "k1"})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Fatalf("expectations: %v", err)
		}
	})

	t.Run("ownership_db_error_is_internal", func(t *testing.T) {
		s, mock, done := newMockServer(t)
		defer done()
		mock.ExpectQuery("SELECT EXISTS").
			WithArgs("s1", "u1", "t1").
			WillReturnError(errors.New("connection reset"))
		_, err := s.DeactivateStreamKey(ctxAs("u1", "t1", "owner"), &commodorepb.DeactivateStreamKeyRequest{StreamId: "s1", KeyId: "k1"})
		wantCode(t, err, codes.Internal)
	})
}

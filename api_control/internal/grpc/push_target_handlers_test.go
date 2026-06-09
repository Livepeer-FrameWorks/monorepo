package grpc

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	fieldcrypt "github.com/Livepeer-FrameWorks/monorepo/pkg/crypto"
	commodorepb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/commodore"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// newPushTargetTestServer wires a sqlmock DB plus a real field encryptor so the
// encrypt-on-write / decrypt-on-read round-trip can be exercised end to end.
func newPushTargetTestServer(t *testing.T) (*CommodoreServer, sqlmock.Sqlmock, *fieldcrypt.FieldEncryptor, func()) {
	t.Helper()
	s, mock, done := newMockServer(t)
	enc, err := fieldcrypt.DeriveFieldEncryptor([]byte("test-master-secret"), "push-target")
	if err != nil {
		t.Fatalf("derive encryptor: %v", err)
	}
	s.fieldEncryptor = enc
	return s, mock, enc, done
}

func pushTargetRows() *sqlmock.Rows {
	return sqlmock.NewRows([]string{
		"id", "stream_id", "platform", "name", "target_uri", "is_enabled",
		"status", "last_error", "last_pushed_at", "created_at", "updated_at",
	})
}

func TestCreatePushTarget(t *testing.T) {
	t.Run("unauthenticated", func(t *testing.T) {
		s, _, _, done := newPushTargetTestServer(t)
		defer done()
		_, err := s.CreatePushTarget(context.Background(), &commodorepb.CreatePushTargetRequest{StreamId: "s1", Name: "n", TargetUri: "rtmp://x/y/z"})
		wantCode(t, err, codes.Unauthenticated)
	})

	t.Run("missing_required_fields", func(t *testing.T) {
		s, _, _, done := newPushTargetTestServer(t)
		defer done()
		ctx := ctxAs("u1", "t1", "owner")
		for _, req := range []*commodorepb.CreatePushTargetRequest{
			{Name: "n", TargetUri: "rtmp://x/y/z"},      // no stream_id
			{StreamId: "s1", TargetUri: "rtmp://x/y/z"}, // no name
			{StreamId: "s1", Name: "n"},                 // no target_uri
		} {
			if _, err := s.CreatePushTarget(ctx, req); status.Code(err) != codes.InvalidArgument {
				t.Errorf("req %+v: expected InvalidArgument, got %v", req, err)
			}
		}
	})

	t.Run("rejects_disallowed_scheme", func(t *testing.T) {
		s, _, _, done := newPushTargetTestServer(t)
		defer done()
		// http is not in validPushSchemes (rtmp/rtmps/srt) — must be rejected
		// before any DB work.
		_, err := s.CreatePushTarget(ctxAs("u1", "t1", "owner"),
			&commodorepb.CreatePushTargetRequest{StreamId: "s1", Name: "n", TargetUri: "http://evil/x"})
		wantCode(t, err, codes.InvalidArgument)
	})

	t.Run("not_found_when_not_owner", func(t *testing.T) {
		s, mock, _, done := newPushTargetTestServer(t)
		defer done()
		mock.ExpectQuery("SELECT EXISTS").
			WithArgs("s1", "u1", "t1").
			WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(false))
		_, err := s.CreatePushTarget(ctxAs("u1", "t1", "owner"),
			&commodorepb.CreatePushTargetRequest{StreamId: "s1", Name: "n", TargetUri: "rtmp://live/app/secretkey"})
		wantCode(t, err, codes.NotFound)
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Fatalf("expectations: %v", err)
		}
	})

	t.Run("happy_path_encrypts_and_masks_response", func(t *testing.T) {
		s, mock, _, done := newPushTargetTestServer(t)
		defer done()
		mock.ExpectQuery("SELECT EXISTS").
			WithArgs("s1", "u1", "t1").
			WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(true))
		// The stored target_uri arg must be the ciphertext, never the plaintext.
		mock.ExpectExec("INSERT INTO commodore.push_targets").
			WithArgs(sqlmock.AnyArg(), "t1", "s1", "custom", "n", encryptedArg{s.fieldEncryptor}, sqlmock.AnyArg()).
			WillReturnResult(sqlmock.NewResult(0, 1))
		expectOutboxInsert(mock)

		resp, err := s.CreatePushTarget(ctxAs("u1", "t1", "owner"),
			&commodorepb.CreatePushTargetRequest{StreamId: "s1", Name: "n", TargetUri: "rtmp://live.twitch.tv/app/live_abc123def"})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		// Response masks the secret tail; it must not echo the raw key.
		if resp.GetTargetUri() == "rtmp://live.twitch.tv/app/live_abc123def" {
			t.Error("response leaked unmasked target_uri")
		}
		if resp.GetTargetUri() != maskTargetURI("rtmp://live.twitch.tv/app/live_abc123def") {
			t.Errorf("target_uri = %q, want masked form", resp.GetTargetUri())
		}
		if !resp.GetIsEnabled() || resp.GetStatus() != "idle" || resp.GetPlatform() != "custom" {
			t.Errorf("unexpected defaults: %+v", resp)
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Fatalf("expectations: %v", err)
		}
	})
}

func TestListPushTargets(t *testing.T) {
	t.Run("unauthenticated", func(t *testing.T) {
		s, _, _, done := newPushTargetTestServer(t)
		defer done()
		_, err := s.ListPushTargets(context.Background(), &commodorepb.ListPushTargetsRequest{StreamId: "s1"})
		wantCode(t, err, codes.Unauthenticated)
	})

	t.Run("empty_stream_id", func(t *testing.T) {
		s, _, _, done := newPushTargetTestServer(t)
		defer done()
		_, err := s.ListPushTargets(ctxAs("u1", "t1", "owner"), &commodorepb.ListPushTargetsRequest{})
		wantCode(t, err, codes.InvalidArgument)
	})

	t.Run("happy_path_decrypts_then_masks", func(t *testing.T) {
		s, mock, enc, done := newPushTargetTestServer(t)
		defer done()
		now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
		plain := "rtmp://a.example.com/app/sk_secret_tail"
		stored, err := enc.Encrypt(plain)
		if err != nil {
			t.Fatalf("encrypt: %v", err)
		}
		mock.ExpectQuery("FROM commodore.push_targets").
			WithArgs("s1", "t1").
			WillReturnRows(pushTargetRows().
				AddRow("pt1", "s1", "custom", "twitch", stored, true, "idle", nil, nil, now, now))

		resp, err := s.ListPushTargets(ctxAs("u1", "t1", "owner"), &commodorepb.ListPushTargetsRequest{StreamId: "s1"})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(resp.GetPushTargets()) != 1 {
			t.Fatalf("targets = %d, want 1", len(resp.GetPushTargets()))
		}
		got := resp.GetPushTargets()[0].GetTargetUri()
		if got != maskTargetURI(plain) {
			t.Errorf("target_uri = %q, want masked %q", got, maskTargetURI(plain))
		}
		if got == plain {
			t.Error("list response leaked the unmasked decrypted URI")
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Fatalf("expectations: %v", err)
		}
	})
}

func TestGetStreamPushTargets(t *testing.T) {
	t.Run("missing_args", func(t *testing.T) {
		s, _, _, done := newPushTargetTestServer(t)
		defer done()
		_, err := s.GetStreamPushTargets(context.Background(), &commodorepb.GetStreamPushTargetsRequest{StreamId: "s1"})
		wantCode(t, err, codes.InvalidArgument)
	})

	t.Run("happy_path_returns_unmasked_decrypted", func(t *testing.T) {
		s, mock, enc, done := newPushTargetTestServer(t)
		defer done()
		plain := "rtmp://a.example.com/app/sk_secret_tail"
		stored, err := enc.Encrypt(plain)
		if err != nil {
			t.Fatalf("encrypt: %v", err)
		}
		// This is the internal Foghorn-facing RPC: it returns the FULL URI so
		// Helmsman can actually push. No masking.
		mock.ExpectQuery("FROM commodore.push_targets").
			WithArgs("s1", "t1").
			WillReturnRows(sqlmock.NewRows([]string{"id", "platform", "name", "target_uri"}).
				AddRow("pt1", "custom", "twitch", stored))

		resp, err := s.GetStreamPushTargets(context.Background(),
			&commodorepb.GetStreamPushTargetsRequest{StreamId: "s1", TenantId: "t1"})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(resp.GetPushTargets()) != 1 {
			t.Fatalf("targets = %d, want 1", len(resp.GetPushTargets()))
		}
		if resp.GetPushTargets()[0].GetTargetUri() != plain {
			t.Errorf("internal target_uri = %q, want full plaintext %q", resp.GetPushTargets()[0].GetTargetUri(), plain)
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Fatalf("expectations: %v", err)
		}
	})
}

func TestUpdatePushTarget(t *testing.T) {
	t.Run("unauthenticated", func(t *testing.T) {
		s, _, _, done := newPushTargetTestServer(t)
		defer done()
		_, err := s.UpdatePushTarget(context.Background(), &commodorepb.UpdatePushTargetRequest{Id: "pt1"})
		wantCode(t, err, codes.Unauthenticated)
	})

	t.Run("empty_id", func(t *testing.T) {
		s, _, _, done := newPushTargetTestServer(t)
		defer done()
		_, err := s.UpdatePushTarget(ctxAs("u1", "t1", "owner"), &commodorepb.UpdatePushTargetRequest{})
		wantCode(t, err, codes.InvalidArgument)
	})

	t.Run("rejects_bad_uri", func(t *testing.T) {
		s, _, _, done := newPushTargetTestServer(t)
		defer done()
		bad := "ftp://nope/x"
		_, err := s.UpdatePushTarget(ctxAs("u1", "t1", "owner"),
			&commodorepb.UpdatePushTargetRequest{Id: "pt1", TargetUri: &bad})
		wantCode(t, err, codes.InvalidArgument)
	})

	t.Run("not_found", func(t *testing.T) {
		s, mock, _, done := newPushTargetTestServer(t)
		defer done()
		name := "renamed"
		mock.ExpectQuery("UPDATE commodore.push_targets").
			WillReturnError(sql.ErrNoRows)
		_, err := s.UpdatePushTarget(ctxAs("u1", "t1", "owner"),
			&commodorepb.UpdatePushTargetRequest{Id: "pt1", Name: &name})
		wantCode(t, err, codes.NotFound)
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Fatalf("expectations: %v", err)
		}
	})

	t.Run("happy_path_updates_and_emits", func(t *testing.T) {
		s, mock, enc, done := newPushTargetTestServer(t)
		defer done()
		now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
		plain := "rtmp://a.example.com/app/sk_secret_tail"
		stored, err := enc.Encrypt(plain)
		if err != nil {
			t.Fatalf("encrypt: %v", err)
		}
		name := "renamed"
		mock.ExpectQuery("UPDATE commodore.push_targets").
			WillReturnRows(pushTargetRows().
				AddRow("pt1", "s1", "custom", name, stored, true, "idle", nil, nil, now, now))
		expectOutboxInsert(mock)

		resp, err := s.UpdatePushTarget(ctxAs("u1", "t1", "owner"),
			&commodorepb.UpdatePushTargetRequest{Id: "pt1", Name: &name})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if resp.GetName() != name {
			t.Errorf("name = %q, want %q", resp.GetName(), name)
		}
		if resp.GetTargetUri() != maskTargetURI(plain) {
			t.Errorf("target_uri = %q, want masked", resp.GetTargetUri())
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Fatalf("expectations: %v", err)
		}
	})
}

func TestDeletePushTarget(t *testing.T) {
	t.Run("unauthenticated", func(t *testing.T) {
		s, _, _, done := newPushTargetTestServer(t)
		defer done()
		_, err := s.DeletePushTarget(context.Background(), &commodorepb.DeletePushTargetRequest{Id: "pt1"})
		wantCode(t, err, codes.Unauthenticated)
	})

	t.Run("empty_id", func(t *testing.T) {
		s, _, _, done := newPushTargetTestServer(t)
		defer done()
		_, err := s.DeletePushTarget(ctxAs("u1", "t1", "owner"), &commodorepb.DeletePushTargetRequest{})
		wantCode(t, err, codes.InvalidArgument)
	})

	t.Run("not_found", func(t *testing.T) {
		s, mock, _, done := newPushTargetTestServer(t)
		defer done()
		mock.ExpectQuery("DELETE FROM commodore.push_targets").
			WithArgs("pt1", "t1").
			WillReturnError(sql.ErrNoRows)
		_, err := s.DeletePushTarget(ctxAs("u1", "t1", "owner"), &commodorepb.DeletePushTargetRequest{Id: "pt1"})
		wantCode(t, err, codes.NotFound)
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Fatalf("expectations: %v", err)
		}
	})

	t.Run("happy_path_deletes_and_emits", func(t *testing.T) {
		s, mock, _, done := newPushTargetTestServer(t)
		defer done()
		mock.ExpectQuery("DELETE FROM commodore.push_targets").
			WithArgs("pt1", "t1").
			WillReturnRows(sqlmock.NewRows([]string{"stream_id"}).AddRow("s1"))
		expectOutboxInsert(mock)

		resp, err := s.DeletePushTarget(ctxAs("u1", "t1", "owner"), &commodorepb.DeletePushTargetRequest{Id: "pt1"})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if resp.GetId() != "pt1" {
			t.Errorf("id = %q, want pt1", resp.GetId())
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Fatalf("expectations: %v", err)
		}
	})
}

// encryptedArg is a sqlmock argument matcher asserting the bound value carries
// the field-encryption prefix — i.e. the handler stored the encrypted form, not
// plaintext. The encryptor field documents which key produced it.
type encryptedArg struct {
	enc *fieldcrypt.FieldEncryptor
}

func (e encryptedArg) Match(v driver.Value) bool {
	s, ok := v.(string)
	if !ok {
		return false
	}
	if _, err := e.enc.Decrypt(s); err != nil {
		return false
	}
	return fieldcrypt.IsEncrypted(s)
}

package grpc

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"strings"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	fieldcrypt "github.com/Livepeer-FrameWorks/monorepo/pkg/crypto"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/ctxkeys"
	commodorepb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/commodore"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// newPushTargetTestServer wires a sqlmock DB plus the production-purpose
// rotating keyring so handler tests exercise v3 writes and mixed-key reads.
func newPushTargetTestServer(t *testing.T) (*CommodoreServer, sqlmock.Sqlmock, *fieldcrypt.FieldKeyring, func()) {
	t.Helper()
	s, mock, done := newMockServer(t)
	enc, err := fieldcrypt.NewFieldKeyring(
		"active", []byte("active-field-key-material-32-bytes"),
		map[string][]byte{"previous": []byte("previous-field-key-material-32-bytes")},
		[][]byte{[]byte("legacy-jwt-key-material-32-bytes")}, "push-target-uri",
	)
	if err != nil {
		t.Fatalf("derive encryptor: %v", err)
	}
	s.fieldEncryptor = enc
	return s, mock, enc, done
}

func pushTargetRows() *sqlmock.Rows {
	return sqlmock.NewRows([]string{
		"id", "stream_id", "platform", "name", "target_uri", "is_enabled",
		"status", "reason_code", "last_error", "last_pushed_at", "created_at", "updated_at",
	})
}

func expectNoEnabledPushTargets(mock sqlmock.Sqlmock, streamID, tenantID string) {
	mock.ExpectQuery("FROM commodore.push_targets").WithArgs(streamID, tenantID).
		WillReturnRows(sqlmock.NewRows([]string{"id", "platform", "name", "target_uri"}))
}

func pushTargetServiceContext() context.Context {
	return context.WithValue(context.Background(), ctxkeys.KeyAuthType, "service")
}

func TestCanManageTenantStreamsDeniesOrdinaryMember(t *testing.T) {
	if canManageTenantStreams(ctxAs("member-user", "tenant-a", "member"), "tenant-a", "streams:write") {
		t.Fatal("ordinary tenant member unexpectedly received stream-management authority")
	}
	if !canManageTenantStreams(ctxAs("owner-user", "tenant-a", "owner"), "tenant-a", "streams:write") {
		t.Fatal("tenant owner did not receive stream-management authority")
	}
	if canManageTenantStreams(ctxAs("owner-user", "tenant-a", "owner"), "tenant-b", "streams:write") {
		t.Fatal("tenant owner unexpectedly received cross-tenant stream-management authority")
	}
}

func TestCanManageTenantStreamsRequiresDelegatedScope(t *testing.T) {
	ctx := ctxAs("owner-user", "tenant-a", "owner")
	ctx = context.WithValue(ctx, ctxkeys.KeyAuthType, "api_token")
	ctx = context.WithValue(ctx, ctxkeys.KeyPermissions, []string{"streams:read"})
	if canManageTenantStreams(ctx, "tenant-a", "streams:write") {
		t.Fatal("read-only API token received tenant-wide stream write authority")
	}
	if !canManageTenantStreams(ctx, "tenant-a", "streams:read") {
		t.Fatal("read-scoped owner token lost tenant-wide stream read authority")
	}
}

func TestPushTargetHandlersRejectUnderScopedAPITokensBeforeDatabaseAccess(t *testing.T) {
	s, mock, _, done := newPushTargetTestServer(t)
	defer done()
	ctx := ctxAs("owner-user", "tenant-a", "owner")
	ctx = context.WithValue(ctx, ctxkeys.KeyAuthType, "api_token")
	ctx = context.WithValue(ctx, ctxkeys.KeyPermissions, []string{"analytics:read"})

	tests := []struct {
		name string
		call func() error
	}{
		{name: "create", call: func() error {
			_, err := s.CreatePushTarget(ctx, &commodorepb.CreatePushTargetRequest{StreamId: "stream-1", Name: "target", TargetUri: "rtmp://example.test/live/key"})
			return err
		}},
		{name: "list", call: func() error {
			_, err := s.ListPushTargets(ctx, &commodorepb.ListPushTargetsRequest{StreamId: "stream-1"})
			return err
		}},
		{name: "update", call: func() error {
			_, err := s.UpdatePushTarget(ctx, &commodorepb.UpdatePushTargetRequest{Id: "target-1"})
			return err
		}},
		{name: "delete", call: func() error {
			_, err := s.DeletePushTarget(ctx, &commodorepb.DeletePushTargetRequest{Id: "target-1"})
			return err
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := tt.call(); status.Code(err) != codes.PermissionDenied {
				t.Fatalf("status = %v, want PermissionDenied (err=%v)", status.Code(err), err)
			}
		})
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("under-scoped request reached database: %v", err)
	}
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
			WithArgs("s1", "t1", "u1", true).
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
			WithArgs("s1", "t1", "u1", true).
			WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(true))
		mock.ExpectQuery("FROM commodore.push_targets").
			WithArgs("s1", "t1", "u1", true).
			WillReturnRows(pushTargetRows())
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
		if !resp.GetIsEnabled() || resp.GetStatus() != "idle" || resp.GetReasonCode() != "unspecified" || resp.GetPlatform() != "custom" {
			t.Errorf("unexpected defaults: %+v", resp)
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Fatalf("expectations: %v", err)
		}
	})

	t.Run("duplicate_uri_is_rejected_before_insert", func(t *testing.T) {
		s, mock, enc, done := newPushTargetTestServer(t)
		defer done()
		const uri = "rtmp://live.example.com/app/shared-secret"
		stored, err := enc.Encrypt(uri)
		if err != nil {
			t.Fatal(err)
		}
		now := time.Now()
		mock.ExpectQuery("SELECT EXISTS").WithArgs("s1", "t1", "u1", true).
			WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(true))
		mock.ExpectQuery("FROM commodore.push_targets").WithArgs("s1", "t1", "u1", true).
			WillReturnRows(pushTargetRows().AddRow("pt-existing", "s1", "custom", "existing", stored, true, "idle", "unspecified", nil, nil, now, now))

		_, err = s.CreatePushTarget(ctxAs("u1", "t1", "owner"), &commodorepb.CreatePushTargetRequest{
			StreamId: "s1", Name: "duplicate", TargetUri: uri,
		})
		wantCode(t, err, codes.AlreadyExists)
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Fatal(err)
		}
	})
}

func TestValidatePushTargetURIDoesNotEchoMalformedSecret(t *testing.T) {
	const secret = "canary-stream-secret"
	err := validatePushTargetURI("rtmp://example.test/live/%zz-" + secret)
	if err == nil {
		t.Fatal("malformed URI was accepted")
	}
	if strings.Contains(err.Error(), secret) {
		t.Fatalf("validation error leaked the target credential: %q", err)
	}
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
			WithArgs("s1", "t1", "u1", true).
			WillReturnRows(pushTargetRows().
				AddRow("pt1", "s1", "custom", "twitch", stored, true, "idle", "unspecified", nil, nil, now, now))

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

	t.Run("undecryptable_row_remains_visible_and_deletable", func(t *testing.T) {
		s, mock, _, done := newPushTargetTestServer(t)
		defer done()
		now := time.Now()
		mock.ExpectQuery("FROM commodore.push_targets").WithArgs("s1", "t1", "u1", true).
			WillReturnRows(pushTargetRows().AddRow("pt-broken", "s1", "custom", "broken", "enc:v1:not-valid", true, "failed", "configuration_error", nil, nil, now, now))

		resp, err := s.ListPushTargets(ctxAs("u1", "t1", "owner"), &commodorepb.ListPushTargetsRequest{StreamId: "s1"})
		if err != nil {
			t.Fatalf("one broken credential must not hide the collection: %v", err)
		}
		if len(resp.GetPushTargets()) != 1 || resp.GetPushTargets()[0].GetId() != "pt-broken" || resp.GetPushTargets()[0].GetTargetUri() != "" {
			t.Fatalf("broken row was not returned safely: %+v", resp.GetPushTargets())
		}
		if resp.GetPushTargets()[0].GetLastError() != "target credentials are unavailable" {
			t.Fatalf("broken row diagnostic=%q", resp.GetPushTargets()[0].GetLastError())
		}
		if resp.GetPushTargets()[0].GetReasonCode() != "configuration_error" {
			t.Fatalf("broken row reason code=%q", resp.GetPushTargets()[0].GetReasonCode())
		}
	})

	t.Run("mixed_legacy_previous_and_active_envelopes_are_readable", func(t *testing.T) {
		s, mock, active, done := newPushTargetTestServer(t)
		defer done()
		legacy, err := fieldcrypt.DeriveFieldEncryptor([]byte("legacy-jwt-key-material-32-bytes"), "push-target-uri")
		if err != nil {
			t.Fatal(err)
		}
		previous, err := fieldcrypt.NewFieldKeyring("previous", []byte("previous-field-key-material-32-bytes"), nil, nil, "push-target-uri")
		if err != nil {
			t.Fatal(err)
		}
		legacyStored, _ := legacy.Encrypt("rtmp://legacy.example/live/key")
		previousStored, _ := previous.Encrypt("rtmp://previous.example/live/key")
		activeStored, _ := active.Encrypt("rtmp://active.example/live/key")
		now := time.Now()
		mock.ExpectQuery("FROM commodore.push_targets").WithArgs("s1", "t1", "u1", true).
			WillReturnRows(pushTargetRows().
				AddRow("legacy", "s1", "custom", "legacy", legacyStored, true, "idle", "unspecified", nil, nil, now, now).
				AddRow("previous", "s1", "custom", "previous", previousStored, true, "idle", "unspecified", nil, nil, now, now).
				AddRow("active", "s1", "custom", "active", activeStored, true, "idle", "unspecified", nil, nil, now, now))

		resp, err := s.ListPushTargets(ctxAs("u1", "t1", "owner"), &commodorepb.ListPushTargetsRequest{StreamId: "s1"})
		if err != nil {
			t.Fatal(err)
		}
		if len(resp.GetPushTargets()) != 3 {
			t.Fatalf("mixed-key rows=%d, want 3", len(resp.GetPushTargets()))
		}
		for _, target := range resp.GetPushTargets() {
			if target.GetTargetUri() == "" || target.GetLastError() != "" {
				t.Fatalf("mixed-key target was not opened safely: %+v", target)
			}
		}
	})
}

func TestGetStreamPushTargets(t *testing.T) {
	t.Run("rejects_user_jwt_before_validation", func(t *testing.T) {
		s, _, _, done := newPushTargetTestServer(t)
		defer done()
		ctx := context.WithValue(ctxAs("u1", "t1", "owner"), ctxkeys.KeyAuthType, "jwt")
		_, err := s.GetStreamPushTargets(ctx, &commodorepb.GetStreamPushTargetsRequest{})
		wantCode(t, err, codes.PermissionDenied)
	})

	t.Run("missing_args", func(t *testing.T) {
		s, _, _, done := newPushTargetTestServer(t)
		defer done()
		_, err := s.GetStreamPushTargets(pushTargetServiceContext(), &commodorepb.GetStreamPushTargetsRequest{StreamId: "s1"})
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

		resp, err := s.GetStreamPushTargets(pushTargetServiceContext(),
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
		if resp.PushTargetsComplete == nil || !resp.GetPushTargetsComplete() {
			t.Fatal("complete target response was not marked complete")
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Fatalf("expectations: %v", err)
		}
	})

	t.Run("decryption_failure_marks_entire_target_section_incomplete", func(t *testing.T) {
		s, mock, _, done := newPushTargetTestServer(t)
		defer done()
		mock.ExpectQuery("FROM commodore.push_targets").
			WithArgs("s1", "t1").
			WillReturnRows(sqlmock.NewRows([]string{"id", "platform", "name", "target_uri"}).
				AddRow("pt-broken", "custom", "broken", "enc:v3:missing:not-valid").
				AddRow("pt-good", "youtube", "healthy", "rtmp://example.test/live/key"))

		resp, err := s.GetStreamPushTargets(pushTargetServiceContext(),
			&commodorepb.GetStreamPushTargetsRequest{StreamId: "s1", TenantId: "t1"})
		if err != nil {
			t.Fatal(err)
		}
		if resp.PushTargetsComplete == nil || resp.GetPushTargetsComplete() || len(resp.GetPushTargets()) != 2 {
			t.Fatalf("partial credential response was not failed closed: %+v", resp)
		}
		if resp.GetPushTargets()[0].GetId() != "pt-broken" || resp.GetPushTargets()[0].GetTargetUri() != "" {
			t.Fatalf("broken target identity was not preserved safely: %+v", resp.GetPushTargets()[0])
		}
		if resp.GetPushTargets()[1].GetId() != "pt-good" || resp.GetPushTargets()[1].GetTargetUri() == "" {
			t.Fatalf("healthy target metadata was not preserved: %+v", resp.GetPushTargets()[1])
		}
	})
}

func TestUpdatePushTargetStatusRequiresServiceAuth(t *testing.T) {
	s, _, _, done := newPushTargetTestServer(t)
	defer done()
	ctx := context.WithValue(ctxAs("u1", "t1", "owner"), ctxkeys.KeyAuthType, "jwt")
	_, err := s.UpdatePushTargetStatus(ctx, &commodorepb.UpdatePushTargetStatusRequest{})
	wantCode(t, err, codes.PermissionDenied)
}

func TestSanitizePushTargetStatusErrorPreservesOnlyBoundedReasons(t *testing.T) {
	reasons := map[commodorepb.PushTargetStatusReason]string{
		commodorepb.PushTargetStatusReason_PUSH_TARGET_STATUS_REASON_DESTINATION_REJECTED:  "destination rejected by operator policy",
		commodorepb.PushTargetStatusReason_PUSH_TARGET_STATUS_REASON_NETWORK_ERROR:         "restream delivery interrupted",
		commodorepb.PushTargetStatusReason_PUSH_TARGET_STATUS_REASON_PROCESS_ERROR:         "restream delivery interrupted",
		commodorepb.PushTargetStatusReason_PUSH_TARGET_STATUS_REASON_CAPACITY_EXHAUSTED:    "delivery capacity exhausted",
		commodorepb.PushTargetStatusReason_PUSH_TARGET_STATUS_REASON_CONFIGURATION_ERROR:   "restream target configuration is invalid",
		commodorepb.PushTargetStatusReason_PUSH_TARGET_STATUS_REASON_EDGE_UPGRADE_REQUIRED: "edge sidecar upgrade required for restream activation",
	}
	for reason, want := range reasons {
		if got := pushTargetStatusReasonMessage(reason); got != want {
			t.Errorf("reason %s mapped to %q, want %q", reason, got, want)
		}
	}
	if got := pushTargetStatusReasonMessage(commodorepb.PushTargetStatusReason_PUSH_TARGET_STATUS_REASON_UNSPECIFIED); got != "" {
		t.Fatalf("unspecified reason mapped to %q", got)
	}

	if got := sanitizePushTargetStatusError("delivery capacity exhausted"); got != "delivery capacity exhausted" {
		t.Fatalf("bounded capacity reason collapsed to %q", got)
	}
	if got := pushTargetStatusReasonCode(commodorepb.PushTargetStatusReason_PUSH_TARGET_STATUS_REASON_EDGE_UPGRADE_REQUIRED); got != "edge_upgrade_required" {
		t.Fatalf("edge-upgrade reason code mapped to %q", got)
	}
	if got := sanitizePushTargetStatusError("dial tcp rtmp://example/live/secret"); got != "restream push failed" {
		t.Fatalf("unbounded runtime error was not collapsed: %q", got)
	}
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
				AddRow("pt1", "s1", "custom", name, stored, true, "idle", "unspecified", nil, nil, now, now))
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
			WithArgs("pt1", "t1", "u1", true).
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
			WithArgs("pt1", "t1", "u1", true).
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
	enc fieldcrypt.FieldCipher
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

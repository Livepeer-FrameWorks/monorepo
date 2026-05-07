package grpc

import (
	"context"
	"fmt"
	"net/netip"
	"strings"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/ctxkeys"
	pb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto"
	"github.com/sirupsen/logrus"
)

func TestValidateWebhookURL(t *testing.T) {
	cases := []struct {
		name    string
		url     string
		wantErr string // substring match; empty means must succeed
		skipDNS bool   // skip if the URL would do a real DNS lookup we can't predict
	}{
		{
			name:    "https public host",
			url:     "https://customer.example.com/playback-access",
			wantErr: "",
			skipDNS: true, // depends on real DNS
		},
		{
			name:    "http rejected",
			url:     "http://customer.example.com/access",
			wantErr: "scheme must be https",
		},
		{
			name:    "file scheme rejected",
			url:     "file:///etc/passwd",
			wantErr: "scheme must be https",
		},
		{
			name:    "gopher rejected",
			url:     "gopher://example.com/",
			wantErr: "scheme must be https",
		},
		{
			name:    "userinfo rejected",
			url:     "https://user:pass@customer.example/",
			wantErr: "userinfo not allowed",
		},
		{
			name:    "operator-internal hostname rejected",
			url:     "https://internal.frameworks.network/foo",
			wantErr: "operator-internal",
		},
		{
			name:    "internal TLD rejected",
			url:     "https://something.internal/foo",
			wantErr: "operator-internal",
		},
		{
			name:    "empty url",
			url:     "",
			wantErr: "url required",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if tc.skipDNS && testing.Short() {
				t.Skip("skip DNS-dependent case in -short")
			}
			err := validateWebhookURL(context.Background(), tc.url)
			if tc.wantErr == "" {
				if err != nil && !isDNSResolutionFailure(err) {
					t.Errorf("expected ok or DNS-related error, got %v", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("want error containing %q, got nil", tc.wantErr)
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Errorf("want error containing %q, got %v", tc.wantErr, err)
			}
		})
	}
}

// isDNSResolutionFailure recognizes errors from real DNS lookups inside the
// validator so the "happy path" test doesn't fail on a sandbox without
// network access.
func isDNSResolutionFailure(err error) bool {
	if err == nil {
		return false
	}
	s := err.Error()
	return strings.Contains(s, "dns lookup failed") || strings.Contains(s, "no such host")
}

func TestIsBlockedIP(t *testing.T) {
	cases := []struct {
		addr    string
		blocked bool
	}{
		// Public — must allow
		{"8.8.8.8", false},
		{"2606:4700:4700::1111", false},

		// Loopback / link-local / private
		{"127.0.0.1", true},
		{"::1", true},
		{"169.254.169.254", true},
		{"10.0.0.1", true},
		{"192.168.1.1", true},
		{"172.20.0.1", true},

		// CGNAT
		{"100.64.0.1", true},

		// 0/8
		{"0.1.2.3", true},

		// IPv6 ULA
		{"fc00::1", true},

		// IPv4-mapped private
		{"::ffff:10.0.0.1", true},
	}

	for _, tc := range cases {
		t.Run(tc.addr, func(t *testing.T) {
			ip, err := netip.ParseAddr(tc.addr)
			if err != nil {
				t.Fatalf("parse %q: %v", tc.addr, err)
			}
			if got := isBlockedIP(ip); got != tc.blocked {
				t.Errorf("isBlockedIP(%s) = %v, want %v", tc.addr, got, tc.blocked)
			}
		})
	}
}

func TestPickPolicyTarget(t *testing.T) {
	mk := func(stream, vod, clip string) *pb.SetPlaybackPolicyRequest {
		return &pb.SetPlaybackPolicyRequest{
			StreamId:   stream,
			VodAssetId: vod,
			ClipId:     clip,
		}
	}

	cases := []struct {
		name     string
		req      *pb.SetPlaybackPolicyRequest
		wantKind string
		wantErr  bool
	}{
		{"stream only", mk("stream-1", "", ""), "stream", false},
		{"vod only", mk("", "vod-1", ""), "vod_asset", false},
		{"clip only", mk("", "", "clip-1"), "clip", false},
		{"none set", mk("", "", ""), "", true},
		{"two set", mk("stream-1", "vod-1", ""), "", true},
		{"three set", mk("stream-1", "vod-1", "clip-1"), "", true},
		{"whitespace only counts as none", mk("  ", "", ""), "", true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tgt, err := pickPolicyTarget(tc.req)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("want error, got target %+v", tgt)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if tgt.kind != tc.wantKind {
				t.Errorf("got kind %q, want %q", tgt.kind, tc.wantKind)
			}
		})
	}
}

func TestBuildPolicyJSON(t *testing.T) {
	t.Run("public produces type-only doc", func(t *testing.T) {
		raw, err := buildPolicyJSON("public", &pb.SetPlaybackPolicyRequest{})
		if err != nil {
			t.Fatalf("build: %v", err)
		}
		if !strings.Contains(string(raw), `"type":"public"`) {
			t.Errorf("want type=public, got %s", raw)
		}
		if strings.Contains(string(raw), `"jwt"`) || strings.Contains(string(raw), `"webhook"`) {
			t.Errorf("public doc should not include jwt/webhook blocks: %s", raw)
		}
	})

	t.Run("webhook caps timeout at 10000ms", func(t *testing.T) {
		req := &pb.SetPlaybackPolicyRequest{
			Webhook: &pb.PlaybackWebhookPolicy{
				Url:       "https://customer.example/access",
				TimeoutMs: 60000,
				SecretPt:  "ignored-here",
			},
		}
		raw, err := buildPolicyJSON("webhook", req)
		if err != nil {
			t.Fatalf("build: %v", err)
		}
		if !strings.Contains(string(raw), `"timeout_ms":10000`) {
			t.Errorf("expected timeout cap to 10000, got %s", raw)
		}
		if strings.Contains(string(raw), "ignored-here") {
			t.Errorf("plaintext secret leaked into JSON: %s", raw)
		}
	})

	t.Run("webhook applies default timeout", func(t *testing.T) {
		req := &pb.SetPlaybackPolicyRequest{
			Webhook: &pb.PlaybackWebhookPolicy{Url: "https://customer.example/access"},
		}
		raw, err := buildPolicyJSON("webhook", req)
		if err != nil {
			t.Fatalf("build: %v", err)
		}
		if !strings.Contains(string(raw), `"timeout_ms":5000`) {
			t.Errorf("expected default 5000ms, got %s", raw)
		}
	})

	t.Run("jwt empty body produces empty jwt block", func(t *testing.T) {
		raw, err := buildPolicyJSON("jwt", &pb.SetPlaybackPolicyRequest{})
		if err != nil {
			t.Fatalf("build: %v", err)
		}
		if !strings.Contains(string(raw), `"type":"jwt"`) {
			t.Errorf("want type=jwt: %s", raw)
		}
	})
}

func TestCreateSigningKeyWrapsCountAndInsertInTransactionWithAdvisoryLock(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()

	tenantID := "tenant-1"

	mock.ExpectBegin()
	mock.ExpectExec("pg_advisory_xact_lock").
		WithArgs(tenantID).
		WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectQuery("SELECT COUNT").
		WithArgs(tenantID).
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(0))
	mock.ExpectQuery("INSERT INTO commodore.signing_keys").
		WithArgs(tenantID, sqlmock.AnyArg(), "key-name", sqlmock.AnyArg()).
		WillReturnRows(sqlmock.NewRows([]string{"id", "created_at"}).
			AddRow("00000000-0000-0000-0000-000000000001", time.Now().UTC()))
	mock.ExpectExec("INSERT INTO commodore.signing_key_audit").
		WithArgs(tenantID, sqlmock.AnyArg(), "create", sqlmock.AnyArg(), sqlmock.AnyArg(), sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()

	server := &CommodoreServer{db: db, logger: logrus.New()}
	ctx := context.WithValue(context.Background(), ctxkeys.KeyUserID, "user-1")
	ctx = context.WithValue(ctx, ctxkeys.KeyTenantID, tenantID)

	resp, err := server.CreateSigningKey(ctx, &pb.CreateSigningKeyRequest{Name: "key-name"})
	if err != nil {
		t.Fatalf("CreateSigningKey: %v", err)
	}
	if resp.GetPrivateKeyPem() == "" {
		t.Fatal("expected private key in response")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("sql expectations: %v", err)
	}
}

// TestWriteSigningKeyAuditPropagatesErrorForTxRollback confirms the audit
// write returns the underlying error so the caller can roll back its
// transaction. Audit must be authoritative — either the mutation lands and is
// audited, or neither happens.
func TestWriteSigningKeyAuditPropagatesErrorForTxRollback(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()

	mock.ExpectExec("INSERT INTO commodore.signing_key_audit").
		WithArgs("tenant-1", "kid-1", "revoke", sqlmock.AnyArg(), sqlmock.AnyArg(), sqlmock.AnyArg()).
		WillReturnError(fmt.Errorf("simulated db outage"))

	server := &CommodoreServer{db: db, logger: logrus.New()}
	if err := server.writeSigningKeyAudit(context.Background(), db, "tenant-1", "kid-1", "revoke", "user-1", ""); err == nil {
		t.Fatal("audit failure must propagate so caller can roll back the mutation")
	}

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("sql expectations: %v", err)
	}
}

func TestCreateSigningKeyAtCapRollsBackAndReturnsResourceExhausted(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()

	tenantID := "tenant-1"

	mock.ExpectBegin()
	mock.ExpectExec("pg_advisory_xact_lock").
		WithArgs(tenantID).
		WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectQuery("SELECT COUNT").
		WithArgs(tenantID).
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(activeSigningKeyCap))
	mock.ExpectRollback()

	server := &CommodoreServer{db: db, logger: logrus.New()}
	ctx := context.WithValue(context.Background(), ctxkeys.KeyUserID, "user-1")
	ctx = context.WithValue(ctx, ctxkeys.KeyTenantID, tenantID)

	if _, err := server.CreateSigningKey(ctx, &pb.CreateSigningKeyRequest{Name: "key-name"}); err == nil {
		t.Fatal("want ResourceExhausted, got nil")
	} else if !strings.Contains(err.Error(), "active signing-key cap") {
		t.Fatalf("want cap error, got %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("sql expectations: %v", err)
	}
}

func TestListSigningKeysUsesAfterCursor(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()

	tenantID := "tenant-1"
	afterID := "00000000-0000-0000-0000-000000000002"
	afterCreatedAt := time.Date(2026, 5, 7, 12, 0, 0, 0, time.UTC)
	olderCreatedAt := afterCreatedAt.Add(-time.Minute)

	mock.ExpectQuery("SELECT created_at FROM commodore.signing_keys").
		WithArgs(afterID, tenantID).
		WillReturnRows(sqlmock.NewRows([]string{"created_at"}).AddRow(afterCreatedAt))
	mock.ExpectQuery("FROM commodore.signing_keys").
		WithArgs(tenantID, afterCreatedAt, afterID, 3).
		WillReturnRows(sqlmock.NewRows([]string{
			"id", "kid", "name", "algorithm", "public_key_pem", "status",
			"created_at", "last_used_at", "revoked_at",
		}).
			AddRow("00000000-0000-0000-0000-000000000001", "kid-1", "older", "ES256", "pem", "active", olderCreatedAt, nil, nil))

	server := &CommodoreServer{db: db, logger: logrus.New()}
	ctx := context.WithValue(context.Background(), ctxkeys.KeyUserID, "user-1")
	ctx = context.WithValue(ctx, ctxkeys.KeyTenantID, tenantID)

	resp, err := server.ListSigningKeys(ctx, &pb.ListSigningKeysRequest{Limit: 2, AfterId: afterID})
	if err != nil {
		t.Fatalf("ListSigningKeys: %v", err)
	}
	if got := len(resp.GetSigningKeys()); got != 1 {
		t.Fatalf("got %d keys, want 1", got)
	}
	if got := resp.GetSigningKeys()[0].GetKid(); got != "kid-1" {
		t.Fatalf("got kid %q, want kid-1", got)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("sql expectations: %v", err)
	}
}

func TestEnqueueInvalidationOutboxInsertsPendingRow(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()

	mock.ExpectQuery("INSERT INTO commodore.playback_policy_invalidation_outbox").
		WithArgs("tenant-1", "key_revoked", sqlmock.AnyArg()).
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow("00000000-0000-0000-0000-000000000001"))

	server := &CommodoreServer{db: db, logger: logrus.New()}
	id, err := server.enqueueInvalidationOutbox(context.Background(), db, "tenant-1", "key_revoked", []string{"stream-x"})
	if err != nil {
		t.Fatalf("enqueueInvalidationOutbox: %v", err)
	}
	if id != "00000000-0000-0000-0000-000000000001" {
		t.Fatalf("got id %q", id)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("sql expectations: %v", err)
	}
}

// TestEnqueueInvalidationOutboxAcceptsSlugClusterIDs confirms the schema
// accepts operator-defined cluster IDs (the slugs used everywhere else in the
// codebase) rather than UUID-only — the original review caught this.
func TestEnqueueInvalidationOutboxAcceptsSlugClusterIDs(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()

	// In the per-mutation model, cluster slugs only appear in the failed-
	// clusters JSON column on retry. Confirm the failure recorder accepts
	// them verbatim.
	mock.ExpectExec("UPDATE commodore.playback_policy_invalidation_outbox").
		WithArgs(1, sqlmock.AnyArg(), "dial: connection refused", `["demo-media","peer-media"]`, "outbox-1").
		WillReturnResult(sqlmock.NewResult(0, 1))

	server := &CommodoreServer{db: db, logger: logrus.New()}
	server.recordInvalidationOutboxFailure(
		context.Background(),
		"outbox-1",
		0,
		[]string{"demo-media", "peer-media"},
		fmt.Errorf("dial: connection refused"),
	)

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("sql expectations: %v", err)
	}
}

// TestRecordInvalidationOutboxFailureRetriesIndefinitely confirms the worker
// has no terminal abandon path. Even after many attempts the row stays
// pending with backoff capped at invalidationOutboxMaxBackoff so a
// partitioned cluster catches up when it returns.
func TestRecordInvalidationOutboxFailureRetriesIndefinitely(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()

	const veryHighAttempts = 100
	mock.ExpectExec("UPDATE commodore.playback_policy_invalidation_outbox").
		WithArgs(veryHighAttempts+1, invalidationOutboxMaxBackoff.Milliseconds(), "permanent failure", `null`, "outbox-1").
		WillReturnResult(sqlmock.NewResult(0, 1))

	server := &CommodoreServer{db: db, logger: logrus.New()}
	server.recordInvalidationOutboxFailure(
		context.Background(),
		"outbox-1",
		veryHighAttempts,
		nil,
		fmt.Errorf("permanent failure"),
	)

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("sql expectations: %v", err)
	}
}

func TestMarkInvalidationOutboxCompletedUpdatesRow(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()

	mock.ExpectExec("UPDATE commodore.playback_policy_invalidation_outbox").
		WithArgs("outbox-1").
		WillReturnResult(sqlmock.NewResult(0, 1))

	server := &CommodoreServer{db: db, logger: logrus.New()}
	server.markInvalidationOutboxCompleted(context.Background(), "outbox-1")

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("sql expectations: %v", err)
	}
}

func TestResolvePlaybackPolicyPublicReadOmitsWebhookSecret(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()

	policyJSON := []byte(`{"type":"webhook","webhook":{"url":"https://customer.example/access","timeout_ms":5000}}`)
	mock.ExpectQuery("FROM commodore.streams WHERE playback_id =").
		WithArgs("playback-1").
		WillReturnRows(sqlmock.NewRows([]string{"playback_policy", "playback_webhook_secret_enc", "tenant_id"}).
			AddRow(policyJSON, "ciphertext", "tenant-1"))

	server := &CommodoreServer{db: db, logger: logrus.New()}
	resp, err := server.ResolvePlaybackPolicy(context.Background(), &pb.ResolvePlaybackPolicyRequest{PlaybackId: "playback-1"})
	if err != nil {
		t.Fatalf("ResolvePlaybackPolicy: %v", err)
	}
	if got := resp.GetWebhookPolicy().GetSecretPt(); got != "" {
		t.Fatalf("public policy read returned webhook secret %q", got)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("sql expectations: %v", err)
	}
}

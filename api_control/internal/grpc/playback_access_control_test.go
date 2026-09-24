package grpc

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/ctxkeys"
	commodorepb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/commodore"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/restream"
	"github.com/sirupsen/logrus"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// A SetPlaybackPolicy against a deleted asset (business row present-but-tombstoned or already removed)
// must NOT mutate the row, commit, or dispatch invalidation: the guarded UPDATE — live row AND no
// tombstone marker — matches zero rows and RETURNs nothing, so the handler returns NotFound BEFORE any
// commit. The sqlmock ordering (Begin, guarded UPDATE→no rows, Rollback; no Commit) proves it.
func TestSetPlaybackPolicy_DeletedAssetReturnsNotFoundNoMutation(t *testing.T) {
	s, mock, done := newMockServer(t)
	defer done()

	mock.ExpectBegin()
	mock.ExpectQuery(`(?s)UPDATE commodore.vod_assets AS v.*NOT EXISTS.*artifact_catalog_tombstones.*RETURNING v.vod_hash`).
		WillReturnError(sql.ErrNoRows)
	mock.ExpectQuery(`(?s)SELECT EXISTS.*origin_type = 'dvr_chapter'`).
		WithArgs("vod-1", "t1").
		WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(false))
	mock.ExpectRollback()

	_, err := s.SetPlaybackPolicy(ctxAs("u1", "t1", "owner"), &commodorepb.SetPlaybackPolicyRequest{
		VodAssetId: "vod-1",
		Type:       "public",
	})
	wantCode(t, err, codes.NotFound)
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("unmet: %v", err)
	}
}

func TestSetPlaybackPolicy_DVRChapterReturnsFailedPrecondition(t *testing.T) {
	s, mock, done := newMockServer(t)
	defer done()

	mock.ExpectBegin()
	mock.ExpectQuery(`(?s)UPDATE commodore.vod_assets AS v.*origin_type.*dvr_chapter`).
		WillReturnError(sql.ErrNoRows)
	mock.ExpectQuery(`(?s)SELECT EXISTS.*origin_type = 'dvr_chapter'`).
		WithArgs("chapter-1", "t1").
		WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(true))
	mock.ExpectRollback()

	_, err := s.SetPlaybackPolicy(ctxAs("u1", "t1", "owner"), &commodorepb.SetPlaybackPolicyRequest{
		VodAssetId: "chapter-1",
		Type:       "public",
	})
	wantCode(t, err, codes.FailedPrecondition)
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("unmet: %v", err)
	}
}

func TestValidateWebhookURL(t *testing.T) {
	cases := []struct {
		name    string
		url     string
		wantErr string // substring match; empty means must succeed
	}{
		{
			name:    "https public host",
			url:     "https://customer.example.com/playback-access",
			wantErr: "",
		},
		{
			name:    "cloud metadata hostname rejected",
			url:     "https://metadata.google/latest",
			wantErr: "not a public destination",
		},
		{
			name:    "loopback literal rejected",
			url:     "https://127.0.0.1/hook",
			wantErr: "not a public destination",
		},
		{
			name:    "http rejected",
			url:     "http://customer.example.com/access",
			wantErr: "url must use https",
		},
		{
			name:    "file scheme rejected",
			url:     "file:///etc/passwd",
			wantErr: "url must use https",
		},
		{
			name:    "gopher rejected",
			url:     "gopher://example.com/",
			wantErr: "url must use https",
		},
		{
			name:    "userinfo rejected",
			url:     "https://user:pass@customer.example/",
			wantErr: "must not contain credentials",
		},
		{
			name:    "operator-internal hostname rejected",
			url:     "https://internal.frameworks.network/foo",
			wantErr: "operator-internal",
		},
		{
			name:    "internal TLD rejected",
			url:     "https://something.internal/foo",
			wantErr: "not a public destination",
		},
		{
			name:    "empty url",
			url:     "",
			wantErr: "url is required",
		},
	}

	policy := resolvingPolicy("93.184.216.34")
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := validateWebhookURL(context.Background(), policy, tc.url)
			if tc.wantErr == "" {
				if err != nil {
					t.Errorf("want accepted, got %v", err)
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

// Literal addresses need no DNS, so these run against the real validator: a
// NAT64 or 6to4 address embedding a private, loopback, or metadata IPv4
// address, the local-use NAT64 prefix, and documentation or benchmarking
// ranges are not public destinations.
func TestValidateWebhookURLRejectsTranslatedAndNonPublicLiterals(t *testing.T) {
	for _, raw := range []string{
		"https://[64:ff9b::7f00:1]/hook",    // NAT64 of 127.0.0.1
		"https://[64:ff9b::a00:1]/hook",     // NAT64 of 10.0.0.1
		"https://[64:ff9b::a9fe:a9fe]/hook", // NAT64 of 169.254.169.254
		"https://[2002:c0a8:101::1]/hook",   // 6to4 of 192.168.1.1
		"https://[64:ff9b:1::a00:1]/hook",   // local-use NAT64 prefix
		"https://192.0.2.10/hook",           // documentation
		"https://198.18.0.1/hook",           // benchmarking
		"https://[2001:db8::1]/hook",        // documentation
	} {
		if err := validateWebhookURL(context.Background(), restream.PublicDestinationPolicy(), raw); err == nil {
			t.Errorf("validateWebhookURL(%q) accepted a non-public destination", raw)
		}
	}
}

// resolvingPolicy is the webhook policy with DNS answering every name with
// the given addresses.
func resolvingPolicy(addrs ...string) restream.DestinationPolicy {
	return restream.DestinationPolicy{LookupIP: func(context.Context, string) ([]net.IP, error) {
		out := make([]net.IP, 0, len(addrs))
		for _, a := range addrs {
			out = append(out, net.ParseIP(a))
		}
		return out, nil
	}}
}

// A hostname is accepted only when every address it resolves to is public,
// including through NAT64 and 6to4 translation, and a resolver failure is
// reported as a lookup failure rather than a policy rejection.
func TestValidateWebhookURLResolvedAddresses(t *testing.T) {
	const raw = "https://hooks.customer.example/playback"
	for _, answer := range [][]string{
		{"10.0.0.8"},
		{"93.184.216.34", "192.168.1.5"},
		{"64:ff9b::a00:8"},
		{"2002:a9fe:a9fe::1"},
		{"::ffff:100.64.0.1"},
		{"fd12::1"},
	} {
		err := validateWebhookURL(context.Background(), resolvingPolicy(answer...), raw)
		if err == nil || !strings.Contains(err.Error(), "not a public destination") {
			t.Errorf("host resolving to %v: err = %v, want a public-destination rejection", answer, err)
		}
	}
	if err := validateWebhookURL(context.Background(), resolvingPolicy("2606:4700:4700::1111", "64:ff9b::5db8:d822"), raw); err != nil {
		t.Fatalf("host resolving to public addresses rejected: %v", err)
	}
	failing := restream.DestinationPolicy{LookupIP: func(context.Context, string) ([]net.IP, error) {
		return nil, errors.New("SERVFAIL")
	}}
	if err := validateWebhookURL(context.Background(), failing, raw); err == nil || !strings.Contains(err.Error(), "dns lookup failed") {
		t.Fatalf("resolver failure: err = %v, want dns lookup failed", err)
	}
}

// SetPlaybackPolicy validates the webhook URL with the server's webhook
// policy before it opens a transaction, and rejects a private answer.
func TestSetPlaybackPolicyRejectsWebhookResolvingPrivate(t *testing.T) {
	s, mock, done := newMockServer(t)
	defer done()
	s.webhookDestinationPolicy = resolvingPolicy("64:ff9b::a00:8")

	_, err := s.SetPlaybackPolicy(ctxAs("u1", "t1", "owner"), &commodorepb.SetPlaybackPolicyRequest{
		StreamId: "stream-1",
		Type:     "webhook",
		Webhook:  &commodorepb.PlaybackWebhookPolicy{Url: "https://hooks.customer.example/playback", SecretPt: "s"},
	})
	wantCode(t, err, codes.InvalidArgument)
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("unmet: %v", err)
	}
}

func TestPickPolicyTarget(t *testing.T) {
	mk := func(stream, vod, clip string) *commodorepb.SetPlaybackPolicyRequest {
		return &commodorepb.SetPlaybackPolicyRequest{
			StreamId:   stream,
			VodAssetId: vod,
			ClipId:     clip,
		}
	}

	cases := []struct {
		name     string
		req      *commodorepb.SetPlaybackPolicyRequest
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
		raw, err := buildPolicyJSON("public", &commodorepb.SetPlaybackPolicyRequest{})
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
		req := &commodorepb.SetPlaybackPolicyRequest{
			Webhook: &commodorepb.PlaybackWebhookPolicy{
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
		req := &commodorepb.SetPlaybackPolicyRequest{
			Webhook: &commodorepb.PlaybackWebhookPolicy{Url: "https://customer.example/access"},
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
		raw, err := buildPolicyJSON("jwt", &commodorepb.SetPlaybackPolicyRequest{})
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

	resp, err := server.CreateSigningKey(ctx, &commodorepb.CreateSigningKeyRequest{Name: "key-name"})
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

	if _, err := server.CreateSigningKey(ctx, &commodorepb.CreateSigningKeyRequest{Name: "key-name"}); err == nil {
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

	resp, err := server.ListSigningKeys(ctx, &commodorepb.ListSigningKeysRequest{Limit: 2, AfterId: afterID})
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
	mock.ExpectQuery("FROM commodore.streams WHERE lower\\(playback_id::text\\) = lower\\(\\$1::text\\)").
		WithArgs("playback-1").
		WillReturnRows(sqlmock.NewRows([]string{"playback_policy", "playback_webhook_secret_enc", "tenant_id"}).
			AddRow(policyJSON, "ciphertext", "tenant-1"))

	server := &CommodoreServer{db: db, logger: logrus.New()}
	resp, err := server.ResolvePlaybackPolicy(ctxAs("user-1", "tenant-1", "member"), &commodorepb.ResolvePlaybackPolicyRequest{PlaybackId: "playback-1"})
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

func TestResolvePlaybackPolicyHidesCrossTenantPolicy(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()

	mock.ExpectQuery("FROM commodore.streams WHERE lower\\(playback_id::text\\) = lower\\(\\$1::text\\)").
		WithArgs("playback-1").
		WillReturnRows(sqlmock.NewRows([]string{"playback_policy", "playback_webhook_secret_enc", "tenant_id"}).
			AddRow([]byte(`{"type":"public"}`), nil, "tenant-owner"))

	server := &CommodoreServer{db: db, logger: logrus.New()}
	_, err = server.ResolvePlaybackPolicy(ctxAs("attacker", "tenant-other", "member"), &commodorepb.ResolvePlaybackPolicyRequest{PlaybackId: "playback-1"})
	if status.Code(err) != codes.NotFound {
		t.Fatalf("cross-tenant resolution error = %v, want NotFound", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("sql expectations: %v", err)
	}
}

func TestResolvePlaybackPolicyWebhookSecretRequiresServiceAuth(t *testing.T) {
	server := &CommodoreServer{logger: logrus.New()}
	_, err := server.ResolvePlaybackPolicy(ctxAs("user-1", "tenant-1", "owner"), &commodorepb.ResolvePlaybackPolicyRequest{
		PlaybackId: "playback-1", IncludeWebhookSecret: true,
	})
	if status.Code(err) != codes.PermissionDenied {
		t.Fatalf("interactive secret resolution error = %v, want PermissionDenied", err)
	}
}

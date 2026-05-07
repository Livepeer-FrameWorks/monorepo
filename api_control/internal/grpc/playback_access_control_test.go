package grpc

import (
	"context"
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

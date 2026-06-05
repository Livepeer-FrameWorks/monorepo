package triggers

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/pem"
	"io"
	"net/netip"
	"testing"
	"time"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	commodorepb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/commodore"
	ipcpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/ipc"
	"github.com/golang-jwt/jwt/v5"
	"github.com/prometheus/client_golang/prometheus"
)

func testPlaybackAuthProcessor() *Processor {
	logger := logging.NewLogger()
	logger.SetOutput(io.Discard)
	return &Processor{logger: logger}
}

func TestLogPlaybackDenyEmitsCounters(t *testing.T) {
	denyTotal := prometheus.NewCounterVec(prometheus.CounterOpts{Name: "test_deny"}, []string{"reason"})
	webhookErr := prometheus.NewCounterVec(prometheus.CounterOpts{Name: "test_webhook"}, []string{"class"})

	p := testPlaybackAuthProcessor()
	p.metrics = &ProcessorMetrics{PlaybackDenyTotal: denyTotal, PlaybackWebhookErrors: webhookErr}

	p.logPlaybackDeny("stream-a", &ipcpb.ViewerConnectTrigger{}, "jwt-expired", "")
	p.logPlaybackDeny("stream-a", &ipcpb.ViewerConnectTrigger{}, "webhook-timeout", "")
	p.logPlaybackDeny("stream-a", &ipcpb.ViewerConnectTrigger{}, "webhook-blocked-ssrf", "")

	if got := counterValue(t, denyTotal.WithLabelValues("jwt-expired")); got != 1 {
		t.Fatalf("jwt-expired count = %v, want 1", got)
	}
	if got := counterValue(t, denyTotal.WithLabelValues("webhook-timeout")); got != 1 {
		t.Fatalf("webhook-timeout deny count = %v, want 1", got)
	}
	if got := counterValue(t, webhookErr.WithLabelValues("timeout")); got != 1 {
		t.Fatalf("webhook timeout class count = %v, want 1", got)
	}
	if got := counterValue(t, webhookErr.WithLabelValues("blocked-ssrf")); got != 1 {
		t.Fatalf("webhook blocked-ssrf class count = %v, want 1", got)
	}
	if got := counterValue(t, webhookErr.WithLabelValues("expired")); got != 0 {
		t.Fatalf("expired (jwt-only) leaked into webhook counter: %v", got)
	}
}

func TestEnforcePlaybackPolicy_PublicMarkerAllowsWithoutCommodore(t *testing.T) {
	p := testPlaybackAuthProcessor()
	got, err := p.enforcePlaybackPolicy(context.Background(), "stream-a", streamContext{
		RequiresAuthKnown: true,
		RequiresAuth:      false,
	}, &ipcpb.ViewerConnectTrigger{})
	if err != nil {
		t.Fatalf("enforcePlaybackPolicy returned error: %v", err)
	}
	if got != "true" {
		t.Fatalf("public marker should allow, got %q", got)
	}
}

func TestEnforcePlaybackPolicy_ProtectedOrUnknownMarkerDenyWithoutCommodore(t *testing.T) {
	p := testPlaybackAuthProcessor()
	cases := []struct {
		name   string
		marker streamContext
	}{
		{"known protected", streamContext{RequiresAuthKnown: true, RequiresAuth: true}},
		{"unknown marker", streamContext{}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := p.enforcePlaybackPolicy(context.Background(), "stream-a", tc.marker, &ipcpb.ViewerConnectTrigger{})
			if err != nil {
				t.Fatalf("enforcePlaybackPolicy returned error: %v", err)
			}
			if got != "false" {
				t.Fatalf("protected/unknown marker should deny without Commodore, got %q", got)
			}
		})
	}
}

func TestEvaluatePlaybackPolicy_JWTMissingTokenDenies(t *testing.T) {
	policy := &commodorepb.ResolvePlaybackPolicyResponse{
		Type: "jwt",
		JwtPolicy: &commodorepb.PlaybackJwtPolicy{
			ActiveKeys: []*commodorepb.PlaybackSigningKey{{Kid: "kid-1", PublicKeyPem: "pem"}},
		},
	}
	got := EvaluatePlaybackPolicy(context.Background(), testPlaybackAuthProcessor().logger, "stream-a", &ipcpb.ViewerConnectTrigger{}, policy)
	if got != "false" {
		t.Fatalf("missing viewer token should deny, got %q", got)
	}
}

func TestEvaluatePlaybackPolicyWithRecorder_RecordsSuccessfulJWTUse(t *testing.T) {
	priv, pubPEM := mustGeneratePlaybackAuthKey(t)
	kid := "kid-record"
	token := mintPlaybackAuthJWT(t, priv, kid)
	recorder := &recordingSigningKeyUseRecorder{calls: make(chan signingKeyUseCall, 1)}
	policy := &commodorepb.ResolvePlaybackPolicyResponse{
		Type:     "jwt",
		TenantId: "tenant-record",
		JwtPolicy: &commodorepb.PlaybackJwtPolicy{
			AllowedKids: []string{kid},
			ActiveKeys:  []*commodorepb.PlaybackSigningKey{{Kid: kid, PublicKeyPem: pubPEM}},
		},
	}

	got := EvaluatePlaybackPolicyWithRecorder(context.Background(), testPlaybackAuthProcessor().logger, "stream-a", &ipcpb.ViewerConnectTrigger{
		ViewerToken: token,
	}, policy, recorder)
	if got != "true" {
		t.Fatalf("valid viewer token should allow, got %q", got)
	}

	select {
	case call := <-recorder.calls:
		if call.tenantID != "tenant-record" || call.kid != kid {
			t.Fatalf("unexpected record call: %+v", call)
		}
	case <-time.After(time.Second):
		t.Fatal("expected signing key use to be recorded")
	}
}

type signingKeyUseCall struct {
	tenantID string
	kid      string
}

type recordingSigningKeyUseRecorder struct {
	calls chan signingKeyUseCall
}

func (r *recordingSigningKeyUseRecorder) RecordSigningKeyUse(ctx context.Context, tenantID, kid string) error {
	select {
	case r.calls <- signingKeyUseCall{tenantID: tenantID, kid: kid}:
	case <-ctx.Done():
		return ctx.Err()
	}
	return nil
}

func mustGeneratePlaybackAuthKey(t *testing.T) (*ecdsa.PrivateKey, string) {
	t.Helper()
	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	pubDER, err := x509.MarshalPKIXPublicKey(&priv.PublicKey)
	if err != nil {
		t.Fatalf("marshal public key: %v", err)
	}
	pubPEM := string(pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: pubDER}))
	return priv, pubPEM
}

func mintPlaybackAuthJWT(t *testing.T, priv *ecdsa.PrivateKey, kid string) string {
	t.Helper()
	token := jwt.NewWithClaims(jwt.SigningMethodES256, jwt.MapClaims{
		"sub": "viewer-record",
		"exp": time.Now().Add(time.Minute).Unix(),
	})
	token.Header["kid"] = kid
	signed, err := token.SignedString(priv)
	if err != nil {
		t.Fatalf("sign token: %v", err)
	}
	return signed
}

func TestIsBlockedDialIP(t *testing.T) {
	cases := []struct {
		name    string
		addr    string
		blocked bool
	}{
		// Public — must allow
		{"public ipv4", "8.8.8.8", false},
		{"public ipv4 cloudflare", "1.1.1.1", false},
		{"public ipv6", "2606:4700:4700::1111", false},

		// Loopback
		{"ipv4 loopback", "127.0.0.1", true},
		{"ipv4 loopback range", "127.99.0.1", true},
		{"ipv6 loopback", "::1", true},

		// RFC1918 private
		{"rfc1918 10/8", "10.1.2.3", true},
		{"rfc1918 172.16/12 low", "172.16.0.1", true},
		{"rfc1918 172.16/12 high", "172.31.255.254", true},
		{"rfc1918 192.168/16", "192.168.1.1", true},

		// Link-local + AWS metadata
		{"link-local ipv4", "169.254.0.1", true},
		{"aws/gcp/azure metadata", "169.254.169.254", true},
		{"link-local ipv6", "fe80::1", true},

		// Unspecified / 0/8
		{"unspecified ipv4", "0.0.0.0", true},
		{"0/8 range", "0.1.2.3", true},

		// CGNAT 100.64.0.0/10
		{"cgnat low", "100.64.0.1", true},
		{"cgnat high", "100.127.255.254", true},

		// IPv6 ULA / link-local
		{"ipv6 ula fc00::/7", "fc00::1", true},
		{"ipv6 ula fd00::", "fd12:3456:789a::1", true},

		// IPv4-mapped IPv6
		{"ipv4-mapped private", "::ffff:10.0.0.1", true},
		{"ipv4-mapped public", "::ffff:8.8.8.8", false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ip, err := netip.ParseAddr(tc.addr)
			if err != nil {
				t.Fatalf("parse %q: %v", tc.addr, err)
			}
			got := isBlockedDialIP(ip)
			if got != tc.blocked {
				t.Errorf("isBlockedDialIP(%s) = %v, want %v", tc.addr, got, tc.blocked)
			}
		})
	}
}

func TestIsBlockedDialIP_Invalid(t *testing.T) {
	// Zero-value Addr (uninitialized) must be treated as blocked — "we don't
	// know what this is, so don't dial it" is the safe default.
	var zero netip.Addr
	if !isBlockedDialIP(zero) {
		t.Error("invalid Addr should be blocked, not allowed")
	}
}

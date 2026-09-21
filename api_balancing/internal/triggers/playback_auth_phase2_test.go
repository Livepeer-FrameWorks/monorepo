package triggers

import (
	"context"
	"crypto/ecdsa"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	commodorepb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/commodore"
	ipcpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/ipc"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/restream"
	"github.com/golang-jwt/jwt/v5"
)

// mintJWTWithClaims mints an ES256 token with caller-chosen claims/kid so tests
// can exercise expired / wrong-audience / valid paths. Reuses the key helpers in
// playback_auth_test.go.
func mintJWTWithClaims(t *testing.T, priv *ecdsa.PrivateKey, kid string, claims jwt.MapClaims) string {
	t.Helper()
	token := jwt.NewWithClaims(jwt.SigningMethodES256, claims)
	token.Header["kid"] = kid
	signed, err := token.SignedString(priv)
	if err != nil {
		t.Fatalf("sign token: %v", err)
	}
	return signed
}

// evaluatePlaybackPolicyDetailed is the top-level playback auth gate. It dispatches
// on policy type; a nil policy or unknown type must DENY (fail closed), public
// allows, and jwt/webhook delegate. This pins the fail-closed dispatch — the most
// important property for a revenue/access gate.
func TestEvaluatePlaybackPolicyDispatch(t *testing.T) {
	logger := testPlaybackAuthProcessor().logger
	ctx := context.Background()
	uv := &ipcpb.ViewerConnectTrigger{}

	t.Run("nil policy denies (fail closed)", func(t *testing.T) {
		d := EvaluatePlaybackPolicyDetailed(ctx, logger, "s", uv, nil, nil)
		if d.Allowed || d.Reason != "policy-empty" {
			t.Fatalf("got allowed=%v reason=%q, want deny policy-empty", d.Allowed, d.Reason)
		}
	})
	t.Run("public allows", func(t *testing.T) {
		d := EvaluatePlaybackPolicyDetailed(ctx, logger, "s", uv, &commodorepb.ResolvePlaybackPolicyResponse{Type: "PUBLIC"}, nil)
		if !d.Allowed || d.PolicyType != "public" {
			t.Fatalf("public policy should allow, got %+v", d)
		}
	})
	t.Run("unknown type denies (fail closed)", func(t *testing.T) {
		d := EvaluatePlaybackPolicyDetailed(ctx, logger, "s", uv, &commodorepb.ResolvePlaybackPolicyResponse{Type: "telepathy"}, nil)
		if d.Allowed || d.Reason != "policy-unknown-type" {
			t.Fatalf("got allowed=%v reason=%q, want deny policy-unknown-type", d.Allowed, d.Reason)
		}
	})
}

// enforceJWTPolicy is the JWT access gate. Every failure mode must deny with a
// specific, stable reason (these surface to operators and metrics): no policy,
// no active keys, expired token, audience mismatch. A valid token allows. On
// deny, the (untrusted) claims are surfaced for the diagnostic panel.
func TestEnforceJWTPolicy(t *testing.T) {
	logger := testPlaybackAuthProcessor().logger
	ctx := context.Background()
	priv, pubPEM := mustGeneratePlaybackAuthKey(t)
	kid := "kid-1"
	jwtPolicy := func() *commodorepb.PlaybackJwtPolicy {
		return &commodorepb.PlaybackJwtPolicy{
			AllowedKids: []string{kid},
			ActiveKeys:  []*commodorepb.PlaybackSigningKey{{Kid: kid, PublicKeyPem: pubPEM}},
		}
	}
	eval := func(policy *commodorepb.ResolvePlaybackPolicyResponse, token string) *PlaybackDecision {
		return EvaluatePlaybackPolicyDetailed(ctx, logger, "s",
			&ipcpb.ViewerConnectTrigger{ViewerToken: token}, policy, nil)
	}

	t.Run("nil jwt policy denies", func(t *testing.T) {
		d := eval(&commodorepb.ResolvePlaybackPolicyResponse{Type: "jwt"}, "tok")
		if d.Allowed || d.Reason != "policy-jwt-empty" {
			t.Fatalf("got %+v, want deny policy-jwt-empty", d)
		}
	})

	t.Run("no active keys denies", func(t *testing.T) {
		d := eval(&commodorepb.ResolvePlaybackPolicyResponse{Type: "jwt", JwtPolicy: &commodorepb.PlaybackJwtPolicy{AllowedKids: []string{kid}}},
			mintJWTWithClaims(t, priv, kid, jwt.MapClaims{"exp": time.Now().Add(time.Minute).Unix()}))
		if d.Allowed || d.Reason != "no-active-keys" {
			t.Fatalf("got %+v, want deny no-active-keys", d)
		}
	})

	t.Run("expired token denies and surfaces claims", func(t *testing.T) {
		tok := mintJWTWithClaims(t, priv, kid, jwt.MapClaims{"sub": "v1", "exp": time.Now().Add(-time.Hour).Unix()})
		d := eval(&commodorepb.ResolvePlaybackPolicyResponse{Type: "jwt", JwtPolicy: jwtPolicy()}, tok)
		if d.Allowed || d.Reason != "jwt-expired" {
			t.Fatalf("got %+v, want deny jwt-expired", d)
		}
		if d.Kid != kid {
			t.Errorf("deny should still surface kid, got %q", d.Kid)
		}
	})

	t.Run("audience mismatch denies", func(t *testing.T) {
		policy := &commodorepb.ResolvePlaybackPolicyResponse{Type: "jwt", JwtPolicy: jwtPolicy()}
		policy.JwtPolicy.RequiredAudience = []string{"expected-aud"}
		tok := mintJWTWithClaims(t, priv, kid, jwt.MapClaims{"sub": "v1", "aud": "wrong-aud", "exp": time.Now().Add(time.Minute).Unix()})
		d := eval(policy, tok)
		if d.Allowed || d.Reason != "jwt-aud-mismatch" {
			t.Fatalf("got %+v, want deny jwt-aud-mismatch", d)
		}
	})

	t.Run("valid token allows", func(t *testing.T) {
		tok := mintJWTWithClaims(t, priv, kid, jwt.MapClaims{"sub": "v1", "exp": time.Now().Add(time.Minute).Unix()})
		d := eval(&commodorepb.ResolvePlaybackPolicyResponse{Type: "jwt", JwtPolicy: jwtPolicy()}, tok)
		if !d.Allowed || d.Kid != kid {
			t.Fatalf("valid token should allow, got %+v", d)
		}
	})
}

// enforceWebhookPolicy: a missing URL fails closed, and the SSRF-hardened client
// must REFUSE to dial a loopback/private webhook target (a customer cannot point
// the policy at 127.0.0.1 or cloud-metadata). The 200/403/5xx response-mapping
// branches sit behind the SSRF-hardened dialer (no injection seam) and can't be
// reached from a loopback test server — see NOTE below; that status switch is the
// prime surgical-extraction candidate.
func TestEnforceWebhookPolicy(t *testing.T) {
	logger := testPlaybackAuthProcessor().logger
	ctx := context.Background()
	uv := &ipcpb.ViewerConnectTrigger{StreamName: "s", SessionId: "sess"}

	t.Run("missing url fails closed", func(t *testing.T) {
		d := EvaluatePlaybackPolicyDetailed(ctx, logger, "s", uv,
			&commodorepb.ResolvePlaybackPolicyResponse{Type: "webhook", WebhookPolicy: &commodorepb.PlaybackWebhookPolicy{}}, nil)
		if d.Allowed || d.Reason != "webhook-no-url" {
			t.Fatalf("got %+v, want deny webhook-no-url", d)
		}
	})

	t.Run("loopback target is SSRF-blocked", func(t *testing.T) {
		// 127.0.0.1 is loopback, so the destination policy refuses the dial.
		d := EvaluatePlaybackPolicyDetailed(ctx, logger, "s", uv,
			&commodorepb.ResolvePlaybackPolicyResponse{Type: "webhook", WebhookPolicy: &commodorepb.PlaybackWebhookPolicy{
				Url:       "http://127.0.0.1:9/hook", // port 9 (discard); never connects — SSRF Control fires first
				SecretPt:  "shh",
				TimeoutMs: 500,
			}}, nil)
		if d.Allowed {
			t.Fatalf("loopback webhook must not be allowed, got %+v", d)
		}
		if !strings.Contains(d.Reason, "webhook-blocked-ssrf") {
			t.Fatalf("reason = %q, want webhook-blocked-ssrf", d.Reason)
		}
	})

	// NAT64 (64:ff9b::/96) and 6to4 (2002::/16) addresses reach the IPv4
	// address they embed, so a private or loopback embedded address is refused
	// at dial time like the IPv4 form.
	for _, target := range []string{
		"http://[64:ff9b::7f00:1]:9/hook",    // NAT64 of 127.0.0.1
		"http://[64:ff9b::a00:1]:9/hook",     // NAT64 of 10.0.0.1
		"http://[64:ff9b::a9fe:a9fe]:9/hook", // NAT64 of 169.254.169.254
		"http://[2002:a00:1::1]:9/hook",      // 6to4 of 10.0.0.1
		"http://[64:ff9b:1::a00:1]:9/hook",   // local-use NAT64 prefix
		"http://192.0.2.1:9/hook",            // documentation range
	} {
		t.Run("translated or non-public target is SSRF-blocked "+target, func(t *testing.T) {
			d := EvaluatePlaybackPolicyDetailed(ctx, logger, "s", uv,
				&commodorepb.ResolvePlaybackPolicyResponse{Type: "webhook", WebhookPolicy: &commodorepb.PlaybackWebhookPolicy{
					Url: target, SecretPt: "shh", TimeoutMs: 500,
				}}, nil)
			if d.Allowed || d.Reason != "webhook-blocked-ssrf" {
				t.Fatalf("got %+v, want deny webhook-blocked-ssrf", d)
			}
		})
	}

	// DNS rebinding: Commodore validates the hostname when the policy is
	// saved, but the name can answer differently when Foghorn dials. The
	// address check runs on the connection itself, so a name that now
	// resolves to loopback is refused although a live receiver there would
	// allow the viewer.
	t.Run("hostname resolving to loopback at dial time is SSRF-blocked", func(t *testing.T) {
		receiver := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusOK)
		}))
		defer receiver.Close()
		_, port, err := net.SplitHostPort(receiver.Listener.Addr().String())
		if err != nil {
			t.Fatal(err)
		}
		d := EvaluatePlaybackPolicyDetailed(ctx, logger, "s", uv,
			&commodorepb.ResolvePlaybackPolicyResponse{Type: "webhook", WebhookPolicy: &commodorepb.PlaybackWebhookPolicy{
				Url: "http://localhost:" + port + "/hook", SecretPt: "shh", TimeoutMs: 2000,
			}}, nil)
		if d.Allowed || d.Reason != "webhook-blocked-ssrf" {
			t.Fatalf("got %+v, want deny webhook-blocked-ssrf", d)
		}
	})
}

// The webhook client returns a redirect instead of following it: the
// redirect target is a new destination the policy never saw. The receiver is
// excepted by CIDR only so a local server can answer.
func TestPlaybackWebhookClientDoesNotFollowRedirects(t *testing.T) {
	var hits atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer target.Close()
	redirector := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL, http.StatusFound)
	}))
	defer redirector.Close()
	client := newPlaybackWebhookClient(time.Second, restream.DestinationPolicy{
		AllowedCIDRs: []*net.IPNet{{IP: net.ParseIP("127.0.0.1").To4(), Mask: net.CIDRMask(32, 32)}},
	})
	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, redirector.URL, nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("request to the excepted receiver: %v", err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusFound || hits.Load() != 0 {
		t.Fatalf("status = %d, redirect target hits = %d; want 302 and 0", resp.StatusCode, hits.Load())
	}
}

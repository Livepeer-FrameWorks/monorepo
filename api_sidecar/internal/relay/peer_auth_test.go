package relay

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/golang-jwt/jwt/v5"

	"frameworks/api_sidecar/internal/admission"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/auth"
	pb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto"
)

// mustMintExpiredArtifactRelayJWT signs an artifact_relay JWT with an
// exp in the past so tests can exercise the validator's expired path
// without sleeping.
func mustMintExpiredArtifactRelayJWT(t *testing.T, secret, audNode, artifactHash, path string) string {
	t.Helper()
	claims := jwt.MapClaims{
		"purpose":       auth.ArtifactRelayPurpose,
		"artifact_hash": artifactHash,
		"path":          path,
		"aud":           audNode,
		"exp":           time.Now().Add(-1 * time.Minute).Unix(),
		"iat":           time.Now().Add(-2 * time.Minute).Unix(),
	}
	signed, err := jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString([]byte(secret))
	if err != nil {
		t.Fatalf("mint expired: %v", err)
	}
	return signed
}

const (
	testPeerNode     = "edge-test-1"
	testPeerSecret   = "peer-relay-test-secret"
	testPeerHash     = "abc123"
	testPeerFile     = "abc123.mp4"
	testPeerReqPath  = "/internal/artifact/vod/abc123.mp4"
	testPeerOrigin   = "test-origin-cluster"
	testPeerCallerCl = "test-peer-cluster"
)

func TestIsLoopbackRemoteAddr(t *testing.T) {
	cases := []struct {
		in   string
		want bool
	}{
		{"127.0.0.1:1234", true},
		{"[::1]:8080", true},
		{"127.0.0.1", true},
		{"::1", true},
		{"10.0.0.1:443", false},
		{"192.168.1.5:80", false},
		{"8.8.8.8:53", false},
		{"", false},
		{"not-an-ip", false},
	}
	for _, c := range cases {
		if got := isLoopbackRemoteAddr(c.in); got != c.want {
			t.Errorf("isLoopbackRemoteAddr(%q)=%v want %v", c.in, got, c.want)
		}
	}
}

func TestArtifactHashFromPath(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"/internal/artifact/vod/abc123.mp4", "abc123"},
		{"/internal/artifact/upload/xyz.mkv", "xyz"},
		{"/internal/artifact/clip/streamX/abc123.mp4", "abc123"},
		{"/internal/artifact/vod/abc123", "abc123"},
		{"/", ""},
		{"", ""},
	}
	for _, c := range cases {
		if got := artifactHashFromPath(c.in); got != c.want {
			t.Errorf("artifactHashFromPath(%q)=%q want %q", c.in, got, c.want)
		}
	}
}

func newAuthTestServer(t *testing.T, withAuth bool) *Server {
	t.Helper()
	opts := Options{
		BasePath: t.TempDir(),
		Admitter: &fakeAdmitter{decision: admission.CacheToDisk},
		Resolver: &fakeResolver{out: map[string]*ResolveResult{
			"vod/" + testPeerHash: {
				State:             pb.AssetState_ASSET_STATE_SOURCE_MISSING,
				ExpectedSizeBytes: 0,
			},
		}},
	}
	if withAuth {
		opts.NodeID = testPeerNode
		opts.RelayAuthSecret = []byte(testPeerSecret)
	}
	return New(opts)
}

func newAuthRouter(s *Server) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	s.MountRoutes(r)
	return r
}

// doSynthGet builds a request with a controlled RemoteAddr and serves it
// through the in-process Gin engine. Avoids httptest.NewServer so we can
// simulate non-loopback callers.
func doSynthGet(r *gin.Engine, remoteAddr, path, authHeader string) *httptest.ResponseRecorder {
	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, path, nil)
	req.RemoteAddr = remoteAddr
	if authHeader != "" {
		req.Header.Set("Authorization", authHeader)
	}
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

func TestPeerAuthMiddleware_LoopbackBypassesEvenWithoutSecret(t *testing.T) {
	s := newAuthTestServer(t, false)
	r := newAuthRouter(s)
	w := doSynthGet(r, "127.0.0.1:9999", testPeerReqPath, "")
	// Loopback bypass means the middleware doesn't gate; the request
	// reaches the relay handler, which then 404s for the
	// SOURCE_MISSING resolve result. Either 404 (resolve miss) or
	// 200/206 is acceptable — what matters is we don't see 401.
	if w.Code == http.StatusUnauthorized {
		t.Fatalf("loopback should bypass auth; got 401")
	}
}

func TestPeerAuthMiddleware_NonLoopbackRejectedWithoutSecret(t *testing.T) {
	s := newAuthTestServer(t, false)
	r := newAuthRouter(s)
	w := doSynthGet(r, "10.0.0.5:54321", testPeerReqPath, "")
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("non-loopback without secret should 401; got %d", w.Code)
	}
}

func TestPeerAuthMiddleware_NonLoopbackRejectedWithoutHeader(t *testing.T) {
	s := newAuthTestServer(t, true)
	r := newAuthRouter(s)
	w := doSynthGet(r, "10.0.0.5:54321", testPeerReqPath, "")
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("missing Authorization should 401; got %d", w.Code)
	}
}

func TestPeerAuthMiddleware_ForwardedHeaderDoesNotBypass(t *testing.T) {
	// Memory: project_mistserver_auth_model.md — spoofed X-Forwarded-For
	// must not unlock the loopback bypass.
	s := newAuthTestServer(t, true)
	r := newAuthRouter(s)
	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, testPeerReqPath, nil)
	req.RemoteAddr = "10.0.0.5:54321"
	req.Header.Set("X-Forwarded-For", "127.0.0.1")
	req.Header.Set("X-Real-IP", "127.0.0.1")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("spoofed forwarded header should not bypass loopback gate; got %d", w.Code)
	}
}

// TestPeerAuthMiddleware_CaddyProxiedLoopbackRequiresJWT covers the
// production deployment shape: Caddy on the per-node FQDN reverse-
// proxies /internal/artifact/* to Helmsman over localhost:18007.
// RemoteAddr at Helmsman is loopback, BUT the request carries
// X-Forwarded-* headers Caddy set. The bypass must NOT trigger; the
// JWT is the security boundary for external requests.
func TestPeerAuthMiddleware_CaddyProxiedLoopbackRequiresJWT(t *testing.T) {
	s := newAuthTestServer(t, true)
	r := newAuthRouter(s)
	cases := []struct {
		name, header, value string
	}{
		{"x-forwarded-for", "X-Forwarded-For", "203.0.113.5"},
		{"x-forwarded-proto", "X-Forwarded-Proto", "https"},
		{"x-forwarded-host", "X-Forwarded-Host", "edge.example.com"},
		{"x-real-ip", "X-Real-Ip", "203.0.113.5"},
		{"forwarded-rfc7239", "Forwarded", `for=203.0.113.5`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, testPeerReqPath, nil)
			req.RemoteAddr = "127.0.0.1:33333"
			req.Header.Set(c.header, c.value)
			w := httptest.NewRecorder()
			r.ServeHTTP(w, req)
			if w.Code != http.StatusUnauthorized {
				t.Fatalf("loopback+%s should require JWT; got %d", c.header, w.Code)
			}
		})
	}
}

func TestPeerAuthMiddleware_ExpiredTokenSurfacesWWWAuthenticate(t *testing.T) {
	s := newAuthTestServer(t, true)
	r := newAuthRouter(s)
	// Mint with a tiny TTL then wait it out, or forge an expired token
	// directly. We forge to avoid sleep in the test.
	token := mustMintExpiredArtifactRelayJWT(t, testPeerSecret, testPeerNode, testPeerHash, testPeerReqPath)
	w := doSynthGet(r, "10.0.0.5:54321", testPeerReqPath, "Bearer "+token)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("expired token should 401; got %d", w.Code)
	}
	got := w.Header().Get("WWW-Authenticate")
	if got != `Bearer error="token_expired"` {
		t.Fatalf("WWW-Authenticate=%q want token_expired marker", got)
	}
}

func TestPeerAuthMiddleware_ValidTokenAccepted(t *testing.T) {
	s := newAuthTestServer(t, true)
	r := newAuthRouter(s)
	token, _, err := auth.GenerateArtifactRelayJWT(testPeerNode, testPeerHash, testPeerReqPath, testPeerOrigin, testPeerCallerCl, 0, []byte(testPeerSecret))
	if err != nil {
		t.Fatalf("mint: %v", err)
	}
	w := doSynthGet(r, "10.0.0.5:54321", testPeerReqPath, "Bearer "+token)
	if w.Code == http.StatusUnauthorized {
		t.Fatalf("valid token should not 401; got body=%q", w.Body.String())
	}
}

func TestPeerAuthMiddleware_WrongNodeAudienceRejected(t *testing.T) {
	s := newAuthTestServer(t, true)
	r := newAuthRouter(s)
	token, _, err := auth.GenerateArtifactRelayJWT("different-node", testPeerHash, testPeerReqPath, testPeerOrigin, testPeerCallerCl, 0, []byte(testPeerSecret))
	if err != nil {
		t.Fatalf("mint: %v", err)
	}
	w := doSynthGet(r, "10.0.0.5:54321", testPeerReqPath, "Bearer "+token)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("wrong-aud token should 401; got %d", w.Code)
	}
}

func TestPeerAuthMiddleware_WrongPathRejected(t *testing.T) {
	s := newAuthTestServer(t, true)
	r := newAuthRouter(s)
	otherPath := "/internal/artifact/vod/other.mp4"
	token, _, err := auth.GenerateArtifactRelayJWT(testPeerNode, "other", otherPath, testPeerOrigin, testPeerCallerCl, 0, []byte(testPeerSecret))
	if err != nil {
		t.Fatalf("mint: %v", err)
	}
	w := doSynthGet(r, "10.0.0.5:54321", testPeerReqPath, "Bearer "+token)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("wrong-path token should 401; got %d", w.Code)
	}
}

func TestBlockServeAuthHeaderAttachedForPeerURL(t *testing.T) {
	// Verify that when ResolveResult carries PeerRelayAuthToken, the
	// upstream block fetch sets Authorization: Bearer.
	gotAuth := ""
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		body := []byte("peer-relay bytes")
		w.Header().Set("Content-Length", "16")
		w.Header().Set("Content-Range", "bytes 0-15/16")
		w.WriteHeader(http.StatusPartialContent)
		_, _ = w.Write(body)
	}))
	defer up.Close()
	dir := t.TempDir()
	res := &ResolveResult{
		State:              pb.AssetState_ASSET_STATE_PLAYABLE,
		PeerRelayURL:       up.URL + "/object",
		PeerRelayAuthToken: "TEST-TOKEN",
		ExpectedSizeBytes:  16,
		ContentType:        "video/mp4",
		URLTTLSeconds:      60,
	}
	resolver := &fakeResolver{out: map[string]*ResolveResult{"vod/abc": res}}
	s := New(Options{
		BasePath:  dir,
		Admitter:  &fakeAdmitter{decision: admission.CacheToDisk},
		Resolver:  resolver,
		BlockSize: 16,
	})
	ts := mount(t, s)
	defer ts.Close()
	resp, err := doGet(t, ts.URL+"/internal/artifact/vod/abc.mp4")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status=%d", resp.StatusCode)
	}
	if gotAuth != "Bearer TEST-TOKEN" {
		t.Fatalf("upstream Authorization=%q want Bearer TEST-TOKEN", gotAuth)
	}
}

func TestBlockServeNoAuthHeaderForS3URL(t *testing.T) {
	gotAuth := ""
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		body := []byte("s3 presigned bytes")
		w.Header().Set("Content-Length", "18")
		w.Header().Set("Content-Range", "bytes 0-17/18")
		w.WriteHeader(http.StatusPartialContent)
		_, _ = w.Write(body)
	}))
	defer up.Close()
	dir := t.TempDir()
	res := &ResolveResult{
		State:             pb.AssetState_ASSET_STATE_PLAYABLE,
		MediaPresignedURL: up.URL + "/object?X-Amz-Algorithm=test",
		ExpectedSizeBytes: 18,
		ContentType:       "video/mp4",
		URLTTLSeconds:     60,
	}
	resolver := &fakeResolver{out: map[string]*ResolveResult{"vod/abc": res}}
	s := New(Options{
		BasePath:  dir,
		Admitter:  &fakeAdmitter{decision: admission.CacheToDisk},
		Resolver:  resolver,
		BlockSize: 32,
	})
	ts := mount(t, s)
	defer ts.Close()
	resp, err := doGet(t, ts.URL+"/internal/artifact/vod/abc.mp4")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status=%d", resp.StatusCode)
	}
	if gotAuth != "" {
		t.Fatalf("upstream Authorization=%q want empty for S3 URL", gotAuth)
	}
}

package relay

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/gin-gonic/gin"

	"frameworks/api_sidecar/internal/admission"
	pb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto"
)

const (
	testPeerNode    = "edge-test-1"
	testPeerHash    = "abc123"
	testPeerReqPath = "/internal/artifact/vod/abc123.mp4"
)

// fakeAuthorizer records calls and returns a canned decision so tests can
// exercise allow / deny / fail-closed without a Foghorn control stream.
type fakeAuthorizer struct {
	allow bool
	err   error

	mu        sync.Mutex
	calls     int
	lastGrant string
	lastHash  string
	lastPath  string
}

func (f *fakeAuthorizer) AuthorizeRelayPull(_ context.Context, grantID, artifactHash, requestPath string) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	f.lastGrant = grantID
	f.lastHash = artifactHash
	f.lastPath = requestPath
	return f.allow, f.err
}

func (f *fakeAuthorizer) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

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

func newAuthTestServer(t *testing.T, authz RelayPullAuthorizer) *Server {
	t.Helper()
	return New(Options{
		BasePath: t.TempDir(),
		Admitter: &fakeAdmitter{decision: admission.CacheToDisk},
		Resolver: &fakeResolver{out: map[string]*ResolveResult{
			"vod/" + testPeerHash: {
				State:             pb.AssetState_ASSET_STATE_SOURCE_MISSING,
				ExpectedSizeBytes: 0,
			},
		}},
		NodeID:     testPeerNode,
		Authorizer: authz,
	})
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

func TestPeerAuthMiddleware_LoopbackBypassesWithoutAuthorize(t *testing.T) {
	// Loopback Mist must never reach the authorizer.
	authz := &fakeAuthorizer{allow: false}
	r := newAuthRouter(newAuthTestServer(t, authz))
	w := doSynthGet(r, "127.0.0.1:9999", testPeerReqPath, "")
	if w.Code == http.StatusUnauthorized {
		t.Fatalf("loopback should bypass; got 401")
	}
	if authz.callCount() != 0 {
		t.Fatalf("loopback must not consult authorizer; calls=%d", authz.callCount())
	}
}

func TestPeerAuthMiddleware_NonLoopbackWithoutGrantRejected(t *testing.T) {
	authz := &fakeAuthorizer{allow: true}
	r := newAuthRouter(newAuthTestServer(t, authz))
	w := doSynthGet(r, "10.0.0.5:54321", testPeerReqPath, "")
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("missing grant should 401; got %d", w.Code)
	}
	if authz.callCount() != 0 {
		t.Fatalf("no grant present → authorizer should not be called; calls=%d", authz.callCount())
	}
}

func TestPeerAuthMiddleware_GrantAllowed(t *testing.T) {
	authz := &fakeAuthorizer{allow: true}
	r := newAuthRouter(newAuthTestServer(t, authz))
	w := doSynthGet(r, "10.0.0.5:54321", testPeerReqPath, "Bearer GRANT-XYZ")
	if w.Code == http.StatusUnauthorized {
		t.Fatalf("allowed grant should not 401; body=%q", w.Body.String())
	}
	if authz.lastGrant != "GRANT-XYZ" || authz.lastHash != testPeerHash || authz.lastPath != testPeerReqPath {
		t.Fatalf("authorizer got (grant=%q hash=%q path=%q)", authz.lastGrant, authz.lastHash, authz.lastPath)
	}
}

// A multi-block pull session reuses one grant+path; the ALLOW must be cached
// so only the first request hits Foghorn.
func TestPeerAuthMiddleware_AllowCachedAcrossRequests(t *testing.T) {
	authz := &fakeAuthorizer{allow: true}
	r := newAuthRouter(newAuthTestServer(t, authz))
	for i := 0; i < 3; i++ {
		w := doSynthGet(r, "10.0.0.5:54321", testPeerReqPath, "Bearer GRANT-XYZ")
		if w.Code == http.StatusUnauthorized {
			t.Fatalf("request %d should be allowed; got 401", i)
		}
	}
	if authz.callCount() != 1 {
		t.Fatalf("authorizer should be consulted once (cached after); calls=%d", authz.callCount())
	}
}

func TestPeerAuthMiddleware_GrantDenied(t *testing.T) {
	authz := &fakeAuthorizer{allow: false}
	r := newAuthRouter(newAuthTestServer(t, authz))
	w := doSynthGet(r, "10.0.0.5:54321", testPeerReqPath, "Bearer GRANT-XYZ")
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("denied grant should 401; got %d", w.Code)
	}
}

func TestPeerAuthMiddleware_AuthorizerErrorFailsClosed(t *testing.T) {
	authz := &fakeAuthorizer{allow: true, err: context.DeadlineExceeded}
	r := newAuthRouter(newAuthTestServer(t, authz))
	w := doSynthGet(r, "10.0.0.5:54321", testPeerReqPath, "Bearer GRANT-XYZ")
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("authorizer error must fail closed (401); got %d", w.Code)
	}
}

// The .dtsh sidecar path must authorize with the BARE artifact hash (the grant
// is minted with the bare hash), not <hash>.<ext>. Guards the double-extension
// regression that previously 401'd every peer .dtsh read.
func TestPeerAuthMiddleware_DtshPathForwardsBareHash(t *testing.T) {
	authz := &fakeAuthorizer{allow: true}
	r := newAuthRouter(newAuthTestServer(t, authz))
	dtshPath := testPeerReqPath + ".dtsh"
	w := doSynthGet(r, "10.0.0.5:54321", dtshPath, "Bearer GRANT-XYZ")
	if w.Code == http.StatusUnauthorized {
		t.Fatalf("allowed .dtsh grant should not 401; got %d", w.Code)
	}
	if authz.lastHash != testPeerHash {
		t.Fatalf(".dtsh authorize hash=%q want bare %q", authz.lastHash, testPeerHash)
	}
	if authz.lastPath != dtshPath {
		t.Fatalf(".dtsh authorize path=%q want %q", authz.lastPath, dtshPath)
	}
}

func TestPeerAuthMiddleware_ForwardedHeaderDoesNotBypass(t *testing.T) {
	// Memory: project_mistserver_auth_model.md — a loopback RemoteAddr with a
	// proxy-forward marker (Caddy proxying remote traffic) must NOT bypass; it
	// goes through the authorizer, which denies here.
	authz := &fakeAuthorizer{allow: false}
	r := newAuthRouter(newAuthTestServer(t, authz))
	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, testPeerReqPath, nil)
	req.RemoteAddr = "127.0.0.1:33333"
	req.Header.Set("X-Forwarded-For", "203.0.113.5")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("loopback+XFF without grant should 401; got %d", w.Code)
	}
}

// Caddy on the per-node FQDN reverse-proxies /internal/artifact/* to Helmsman
// over loopback with X-Forwarded-* set. The bypass must NOT trigger; the
// authorize gate is the boundary for external (peer) requests.
func TestPeerAuthMiddleware_CaddyProxiedLoopbackRequiresAuthorize(t *testing.T) {
	authz := &fakeAuthorizer{allow: false}
	r := newAuthRouter(newAuthTestServer(t, authz))
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
			req.Header.Set("Authorization", "Bearer GRANT-XYZ")
			w := httptest.NewRecorder()
			r.ServeHTTP(w, req)
			if w.Code != http.StatusUnauthorized {
				t.Fatalf("loopback+%s should require authorize (denied); got %d", c.header, w.Code)
			}
		})
	}
}

// Trusted-CIDR bypass: the local Mist→Helmsman hop where Mist dials a
// non-loopback service address (docker: helmsman:18007). In the CIDR without
// proxy markers → bypass (no authorize); with a marker (Caddy proxying peer
// traffic on the same bridge) → authorize path.
func TestPeerAuthMiddleware_TrustedCIDRBypass(t *testing.T) {
	authz := &fakeAuthorizer{allow: false}
	s := New(Options{
		BasePath: t.TempDir(),
		Admitter: &fakeAdmitter{decision: admission.CacheToDisk},
		Resolver: &fakeResolver{out: map[string]*ResolveResult{
			"vod/" + testPeerHash: {State: pb.AssetState_ASSET_STATE_SOURCE_MISSING},
		}},
		NodeID:           testPeerNode,
		Authorizer:       authz,
		RelayTrustedCIDR: "172.16.0.0/12, 10.0.0.0/8",
	})
	r := newAuthRouter(s)

	w := doSynthGet(r, "172.20.0.7:40000", testPeerReqPath, "")
	if w.Code == http.StatusUnauthorized {
		t.Fatalf("trusted-CIDR caller without markers should bypass; got 401")
	}
	if authz.callCount() != 0 {
		t.Fatalf("trusted-CIDR bypass must not consult authorizer; calls=%d", authz.callCount())
	}

	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, testPeerReqPath, nil)
	req.RemoteAddr = "172.20.0.7:40000"
	req.Header.Set("X-Forwarded-For", "203.0.113.5")
	req.Header.Set("Authorization", "Bearer GRANT-XYZ")
	wm := httptest.NewRecorder()
	r.ServeHTTP(wm, req)
	if wm.Code != http.StatusUnauthorized {
		t.Fatalf("trusted-CIDR caller WITH proxy marker must authorize (denied); got %d", wm.Code)
	}

	wo := doSynthGet(r, "192.0.2.9:40000", testPeerReqPath, "")
	if wo.Code != http.StatusUnauthorized {
		t.Fatalf("untrusted CIDR without grant should 401; got %d", wo.Code)
	}
}

func TestBlockServeGrantHeaderAttachedForPeerURL(t *testing.T) {
	// When ResolveResult carries PeerRelayGrantID, the upstream block fetch
	// sets Authorization: Bearer <grant>.
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
		State:             pb.AssetState_ASSET_STATE_PLAYABLE,
		PeerRelayURL:      up.URL + "/object",
		PeerRelayGrantID:  "TEST-GRANT",
		ExpectedSizeBytes: 16,
		ContentType:       "video/mp4",
		URLTTLSeconds:     60,
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
	if gotAuth != "Bearer TEST-GRANT" {
		t.Fatalf("upstream Authorization=%q want Bearer TEST-GRANT", gotAuth)
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

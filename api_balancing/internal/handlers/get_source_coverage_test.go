package handlers

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"frameworks/api_balancing/internal/control"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/ctxkeys"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	clusterpeerpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/cluster_peer"
	commodorepb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/commodore"
	"github.com/gin-gonic/gin"
	"google.golang.org/protobuf/proto"
)

// withLoggerGetSource installs a real package-global logger for the duration of
// the test. handleGetSource and its async postBalancingEvent goroutine both
// dereference the nil-by-default `logger`, so it must be non-nil. We never
// restore it to a nil previous value: the fire-and-forget event goroutine may
// outlive the test and would panic on a nil logger after a naive restore.
func withLoggerGetSource(t *testing.T) {
	t.Helper()
	if logger == nil {
		logger = logging.NewLogger()
	}
}

// newSourceRequestGetSource builds a gin context whose request path/query model
// a Mist /source lookup. path is the raw URL path (e.g. "/source/by-node/edgeA"
// or "/" for the ?source= form); the source stream is carried in the query.
func newSourceRequestGetSource(path string, q url.Values) (*gin.Context, *httptest.ResponseRecorder) {
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	raw := path
	if enc := q.Encode(); enc != "" {
		raw = path + "?" + enc
	}
	c.Request = httptest.NewRequestWithContext(context.Background(), "GET", raw, nil)
	return c, w
}

// handleGetSource: a live+ stream with no node carrying active inputs must
// return "push://" unconditionally. This is the load-bearing live invariant —
// publishers boot the ingest buffer via push, viewers fail the balancer's
// provider pre-check and get a clean offline. Live must never hard-fail to ""
// because a publisher may arrive at any moment.
func TestHandleGetSourceLiveNoNodeReturnsPush(t *testing.T) {
	withSeededBalancer(t)
	withLoggerGetSource(t)

	q := url.Values{}
	c, w := newSourceRequestGetSource("/", q)
	handleGetSource(c, "live+nobody", q)

	if w.Code != 200 {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	if w.Body.String() != "push://" {
		t.Fatalf("live+ no-node body = %q, want push://", w.Body.String())
	}
}

// handleGetSource: a bare/native stream with no carrying node returns the
// caller-supplied ?fallback verbatim (the terminal "offline" answer for
// non-live prefixes). Distinct from live+, which always returns push://.
func TestHandleGetSourceBareNoNodeReturnsFallback(t *testing.T) {
	withSeededBalancer(t)
	withLoggerGetSource(t)

	q := url.Values{}
	q.Set("fallback", "offline:gone")
	c, w := newSourceRequestGetSource("/", q)
	handleGetSource(c, "barenative", q)

	if w.Code != 200 {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	if w.Body.String() != "offline:gone" {
		t.Fatalf("bare no-node body = %q, want the supplied fallback", w.Body.String())
	}
}

// handleGetSource origin resolution: when exactly one node carries the stream
// with active inputs, /source resolves to that node's DTSC upstream
// (dtsc://<host>:4200). This is the core origin-vs-pull-vs-remote decision:
// a present origin wins, and the answer is the origin node's DTSC URL.
func TestHandleGetSourceOriginResolvesToDTSC(t *testing.T) {
	sm := withSeededBalancer(t)
	withLoggerGetSource(t)
	seedNodeWithStream(t, sm, seedNode{
		nodeID: "origin", host: "origin.example", active: true,
		ramMax: 100, ramCur: 10, cpu: 50,
	}, "live+show", 0, 0, 0)

	q := url.Values{}
	c, w := newSourceRequestGetSource("/", q)
	handleGetSource(c, "live+show", q)

	if w.Code != 200 {
		t.Fatalf("status = %d, want 200: body=%q", w.Code, w.Body.String())
	}
	if got := w.Body.String(); got != "dtsc://origin.example:4200" {
		t.Fatalf("origin source body = %q, want dtsc://origin.example:4200", got)
	}
}

// handleGetSource redirect mode: ?redirect=1 turns the origin answer into a
// 302 Found whose Location is the DTSC URL, instead of an inline body. This
// locks the redirect-vs-inline branch of the resolution.
func TestHandleGetSourceRedirectMode(t *testing.T) {
	sm := withSeededBalancer(t)
	withLoggerGetSource(t)
	seedNodeWithStream(t, sm, seedNode{
		nodeID: "origin", host: "origin.example", active: true,
		ramMax: 100, ramCur: 10, cpu: 50,
	}, "live+show", 0, 0, 0)

	q := url.Values{}
	q.Set("redirect", "1")
	c, w := newSourceRequestGetSource("/", q)
	handleGetSource(c, "live+show", q)

	if w.Code != 302 {
		t.Fatalf("status = %d, want 302 redirect", w.Code)
	}
	if loc := w.Header().Get("Location"); loc != "dtsc://origin.example:4200" {
		t.Fatalf("redirect Location = %q, want dtsc://origin.example:4200", loc)
	}
}

func withSourceAuthorizationGetSource(t *testing.T, tenantID string, allowed bool) {
	t.Helper()
	previousResolver := resolveSourceAuthorityFn
	previousAccess := sourceClusterAccessibleForScope
	resolveSourceAuthorityFn = func(context.Context, string) (ctxkeys.ClusterServeScope, bool) {
		return control.NewClusterServeScope(tenantID, "cluster-1", nil), tenantID != ""
	}
	sourceClusterAccessibleForScope = func(clusterID string, scope ctxkeys.ClusterServeScope) bool {
		if clusterID != "cluster-edge-a" || scope.TenantID != tenantID {
			t.Fatalf("source authority evaluated cluster=%q tenant=%q", clusterID, scope.TenantID)
		}
		return allowed
	}
	t.Cleanup(func() {
		resolveSourceAuthorityFn = previousResolver
		sourceClusterAccessibleForScope = previousAccess
	})
}

// A signed public /source capability authenticates an edge cluster, not the
// queried stream. Reject a stream owned by another tenant before local origin
// selection can disclose its DTSC address.
func TestHandleGetSourceRejectsUnauthorizedSignedCallerBeforeOriginDisclosure(t *testing.T) {
	sm := withSeededBalancer(t)
	withLoggerGetSource(t)
	withSourceAuthorizationGetSource(t, "tenant-b", false)
	seedNodeWithStream(t, sm, seedNode{
		nodeID: "origin-b", host: "origin-b.example", active: true,
		ramMax: 100, ramCur: 10, cpu: 50,
	}, "live+tenantb", 0, 0, 0)

	q := url.Values{}
	c, w := newSourceRequestGetSource("/", q)
	c.Request = c.Request.WithContext(context.WithValue(c.Request.Context(), ctxkeys.KeyAuthenticatedNodeCluster, "cluster-edge-a"))
	handleGetSource(c, "live+tenantb", q)

	if w.Code != 200 {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	if got := w.Body.String(); got != "push://" {
		t.Fatalf("unauthorized live source body = %q, want push://", got)
	}
	if strings.Contains(w.Body.String(), "dtsc://") {
		t.Fatalf("unauthorized response disclosed origin: %q", w.Body.String())
	}
}

func TestHandleGetSourceAllowsAuthorizedSignedCaller(t *testing.T) {
	sm := withSeededBalancer(t)
	withLoggerGetSource(t)
	withSourceAuthorizationGetSource(t, "tenant-a", true)
	lb.SetClusterServeAuthorizer(control.ClusterServeAccessibleForScope)
	seedNodeWithStream(t, sm, seedNode{
		nodeID: "origin-a", host: "origin-a.example", active: true,
		tags: []string{"edge"}, ramMax: 100, ramCur: 10, cpu: 50,
	}, "live+tenanta", 0, 0, 0)

	q := url.Values{}
	c, w := newSourceRequestGetSource("/", q)
	c.Request = c.Request.WithContext(context.WithValue(c.Request.Context(), ctxkeys.KeyAuthenticatedNodeCluster, "cluster-edge-a"))
	handleGetSource(c, "live+tenanta", q)

	if got := w.Body.String(); got != "dtsc://origin-a.example:4200" {
		t.Fatalf("authorized source body = %q, want origin DTSC", got)
	}
}

func TestHandleGetSourceRejectsSignedCallerWhenTenantAuthorityUnavailable(t *testing.T) {
	withSeededBalancer(t)
	withLoggerGetSource(t)
	previousResolver := resolveSourceAuthorityFn
	previousAccess := sourceClusterAccessibleForScope
	resolveSourceAuthorityFn = func(context.Context, string) (ctxkeys.ClusterServeScope, bool) {
		return ctxkeys.ClusterServeScope{}, false
	}
	sourceClusterAccessibleForScope = func(string, ctxkeys.ClusterServeScope) bool {
		t.Fatal("cluster access must not run without resolved tenant authority")
		return false
	}
	t.Cleanup(func() {
		resolveSourceAuthorityFn = previousResolver
		sourceClusterAccessibleForScope = previousAccess
	})

	q := url.Values{}
	c, w := newSourceRequestGetSource("/", q)
	c.Request = c.Request.WithContext(context.WithValue(c.Request.Context(), ctxkeys.KeyAuthenticatedNodeCluster, "cluster-edge-a"))
	handleGetSource(c, "live+unknown", q)

	if got := w.Body.String(); got != "push://" {
		t.Fatalf("unresolved live authority body = %q, want push://", got)
	}
}

func TestResolveSourceAuthorityFallsThroughTransientLocalErrorToExactCommodorePolicy(t *testing.T) {
	startBalancingCommodoreFake(t, &commodoreBalancingFake{
		internalName: func(context.Context, *commodorepb.ResolveInternalNameRequest) (*commodorepb.ResolveInternalNameResponse, error) {
			return &commodorepb.ResolveInternalNameResponse{
				TenantId: "tenant-1", OfficialClusterId: "official-1",
				AllowPlatformSharedPlayback: false, ServePolicyResolved: proto.Bool(true),
			}, nil
		},
	})
	previousLocal := resolveLocalSourceContent
	resolveLocalSourceContent = func(context.Context, string) (*control.ContentResolution, bool, error) {
		return nil, true, errors.New("transient local database error")
	}
	t.Cleanup(func() { resolveLocalSourceContent = previousLocal })

	scope, ok := resolveSourceAuthority(context.Background(), "live+demo")
	if !ok || scope.TenantID != "tenant-1" || scope.OfficialClusterID != "official-1" {
		t.Fatalf("fallback scope = (%+v, %v), want exact Commodore tenant policy", scope, ok)
	}
	if scope.AllowPlatformSharedPlayback {
		t.Fatal("Commodore opt-out was widened to platform-shared access")
	}
}

func TestResolveSourceAuthorityPrefersExactLocalServePolicy(t *testing.T) {
	previousLocal := resolveLocalSourceContent
	resolveLocalSourceContent = func(context.Context, string) (*control.ContentResolution, bool, error) {
		return &control.ContentResolution{
			TenantId:                    "tenant-local",
			OfficialClusterID:           "official-local",
			AuthorityClusterPeers:       []*clusterpeerpb.TenantClusterPeer{{ClusterId: "peer-local"}},
			AllowPlatformSharedPlayback: false,
		}, true, nil
	}
	t.Cleanup(func() { resolveLocalSourceContent = previousLocal })

	scope, ok := resolveSourceAuthority(context.Background(), "live+local")
	if !ok || scope.TenantID != "tenant-local" || scope.OfficialClusterID != "official-local" {
		t.Fatalf("local scope = (%+v, %v)", scope, ok)
	}
	if scope.AllowPlatformSharedPlayback || len(scope.PeerClusterIDs) != 1 || scope.PeerClusterIDs[0] != "peer-local" {
		t.Fatalf("local policy was widened or lost: %+v", scope)
	}
}

func TestResolveSourceAuthorityUsesLegacyEnvelopeOnlyWhenPolicyPresenceIsAbsent(t *testing.T) {
	startBalancingCommodoreFake(t, &commodoreBalancingFake{
		internalName: func(context.Context, *commodorepb.ResolveInternalNameRequest) (*commodorepb.ResolveInternalNameResponse, error) {
			return &commodorepb.ResolveInternalNameResponse{
				TenantId: "tenant-legacy", OriginClusterId: "origin-legacy",
				ClusterPeers: []*clusterpeerpb.TenantClusterPeer{{ClusterId: "peer-legacy"}},
			}, nil
		},
	})
	previousLocal := resolveLocalSourceContent
	resolveLocalSourceContent = func(context.Context, string) (*control.ContentResolution, bool, error) {
		return nil, false, nil
	}
	t.Cleanup(func() { resolveLocalSourceContent = previousLocal })

	scope, ok := resolveSourceAuthority(context.Background(), "live+legacy")
	if !ok || scope.TenantID != "tenant-legacy" || scope.OfficialClusterID != "origin-legacy" {
		t.Fatalf("legacy fallback scope = (%+v, %v)", scope, ok)
	}
	if !scope.AllowPlatformSharedPlayback || len(scope.PeerClusterIDs) != 1 || scope.PeerClusterIDs[0] != "peer-legacy" {
		t.Fatalf("legacy fallback did not preserve pre-policy envelope: %+v", scope)
	}
}

func TestResolveSourceAuthorityExplicitUnresolvedPolicyFailsClosed(t *testing.T) {
	startBalancingCommodoreFake(t, &commodoreBalancingFake{
		internalName: func(context.Context, *commodorepb.ResolveInternalNameRequest) (*commodorepb.ResolveInternalNameResponse, error) {
			return &commodorepb.ResolveInternalNameResponse{
				TenantId: "tenant-1", OriginClusterId: "origin-1",
				ServePolicyResolved: proto.Bool(false),
			}, nil
		},
	})
	previousLocal := resolveLocalSourceContent
	resolveLocalSourceContent = func(context.Context, string) (*control.ContentResolution, bool, error) {
		return nil, false, nil
	}
	t.Cleanup(func() { resolveLocalSourceContent = previousLocal })

	if scope, ok := resolveSourceAuthority(context.Background(), "live+denied"); ok {
		t.Fatalf("explicit unresolved policy widened to legacy scope: %+v", scope)
	}
}

func TestAuthorizeSourceCallerUsesPureServeScopeForPlatformSharedCell(t *testing.T) {
	previousResolver := resolveSourceAuthorityFn
	previousAccess := sourceClusterAccessibleForScope
	resolveSourceAuthorityFn = func(context.Context, string) (ctxkeys.ClusterServeScope, bool) {
		return control.NewClusterServeScope("tenant-free", "official-other", nil, true), true
	}
	sourceClusterAccessibleForScope = control.ClusterServeAccessibleForScope
	t.Cleanup(func() {
		resolveSourceAuthorityFn = previousResolver
		sourceClusterAccessibleForScope = previousAccess
	})
	control.AddPlatformSharedCluster("platform-shared-source-auth-test")

	ctx := context.WithValue(context.Background(), ctxkeys.KeyAuthenticatedNodeCluster, "platform-shared-source-auth-test")
	scope, reason := authorizeSourceCaller(ctx, "live+tenant-free")
	if reason != "" || scope.TenantID != "tenant-free" {
		t.Fatalf("authorizeSourceCaller = (%+v, %q), want tenant-free allowed", scope, reason)
	}
}

// handleGetSource pull resolution: a pull+ stream with no Commodore client
// configured cannot resolve its upstream URI and returns the explicit
// "offline: unavailable" sentinel — absence of the central dependency is not
// an authoritative statement that the stream was never configured.
// This locks the pull-kind branch (handleGetPullSource → resolvePullSourceForSource
// nil-Commodore short-circuit) as distinct from origin/remote.
func TestHandleGetSourcePullNoCommodoreOffline(t *testing.T) {
	withSeededBalancer(t)
	withLoggerGetSource(t)

	prevCommodore := control.CommodoreClient
	control.CommodoreClient = nil
	t.Cleanup(func() { control.CommodoreClient = prevCommodore })

	q := url.Values{}
	c, w := newSourceRequestGetSource("/", q)
	handleGetSource(c, "pull+somestream", q)

	if w.Code != 200 {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	if got := w.Body.String(); got != control.OfflineUnavailable {
		t.Fatalf("pull+ no-commodore body = %q, want %q", got, control.OfflineUnavailable)
	}
}

// sourceCallerNodeID extracts the caller node identity from a
// /source/by-node/<id> path, URL-unescaping it and ignoring any trailing
// segment. This is the dest-edge identity that gates active-replication pin
// checks, so the parse must be exact.
func TestSourceCallerNodeIDFromPath(t *testing.T) {
	withSeededBalancer(t) // installs DefaultManager for the fallback branch

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequestWithContext(context.Background(), "GET",
		sourceByNodePathPrefix+"edge%2BA/extra?source=live%2Bshow", nil)

	got := sourceCallerNodeID(c, c.Request.URL.Query(), "203.0.113.9")
	if got != "edge+A" {
		t.Fatalf("caller node id = %q, want edge+A (unescaped, trailing segment dropped)", got)
	}
}

// MistSourceHandler dispatch: the /source/by-node/<id>?source=
// branch routes to handleGetSource. With no node carrying a live+ stream the
// terminal answer is push://, proving the request reached source resolution
// (not stream balancing or an admin handler).
func TestCompatDispatchSourceByNodeRoutesToGetSource(t *testing.T) {
	withSeededBalancer(t)
	withLoggerGetSource(t)

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequestWithContext(context.Background(), "GET",
		sourceByNodePathPrefix+"edgeA?source=live%2Bnobody", nil)

	MistSourceHandler(c)

	if w.Code != 200 {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	if got := w.Body.String(); got != "push://" {
		t.Fatalf("source-by-node dispatch body = %q, want push:// (handleGetSource live+ fallback)", got)
	}
}

// MistSourceHandler dispatch: a /source/by-node/ request with an
// empty source query is rejected with 400 before any resolution — the
// dispatcher guards the missing-source case rather than falling through.
func TestCompatDispatchSourceByNodeMissingSource(t *testing.T) {
	withSeededBalancer(t)
	withLoggerGetSource(t)

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequestWithContext(context.Background(), "GET",
		sourceByNodePathPrefix+"edgeA", nil)

	MistSourceHandler(c)

	if w.Code != 400 {
		t.Fatalf("status = %d, want 400 for missing source", w.Code)
	}
	if !strings.Contains(w.Body.String(), "Missing source") {
		t.Fatalf("body = %q, want Missing source", w.Body.String())
	}
}

// MistSourceHandler dispatch: the HTTP/2 PRI preface and
// /favicon.ico are short-circuited before any stream/source routing. These
// guard the dispatcher's non-balancing branches.
func TestCompatDispatchPRIAndFavicon(t *testing.T) {
	withSeededBalancer(t)
	withLoggerGetSource(t)

	// PRI * preface → 200 empty, no routing.
	wPRI := httptest.NewRecorder()
	cPRI, _ := gin.CreateTestContext(wPRI)
	cPRI.Request = httptest.NewRequestWithContext(context.Background(), "PRI", "/", nil)
	cPRI.Request.RequestURI = "*"
	MistSourceHandler(cPRI)
	if wPRI.Code != 200 || wPRI.Body.String() != "" {
		t.Fatalf("PRI preface = (%d,%q), want (200,\"\")", wPRI.Code, wPRI.Body.String())
	}

	// favicon → 404.
	wFav := httptest.NewRecorder()
	cFav, _ := gin.CreateTestContext(wFav)
	cFav.Request = httptest.NewRequestWithContext(context.Background(), "GET", "/favicon.ico", nil)
	MistSourceHandler(cFav)
	if wFav.Code != 404 {
		t.Fatalf("favicon status = %d, want 404", wFav.Code)
	}
}

// Source lookup never treats an unknown path as a viewer routing request.
func TestCompatDispatchInvalidStreamName(t *testing.T) {
	withSeededBalancer(t)
	withLoggerGetSource(t)

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	// "ab" is too short for the {3,127} regex → invalid.
	c.Request = httptest.NewRequestWithContext(context.Background(), "GET", "/ab", nil)
	MistSourceHandler(c)

	if w.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", w.Code)
	}
}

func TestSourceDispatchRejectsHostnameOnlyViewerRouting(t *testing.T) {
	sm := withSeededBalancer(t)
	withLoggerGetSource(t)
	control.Init(logging.NewLogger(), nil, nil)
	// Seed a node carrying the stream so balancing resolves to it; this also
	// distinguishes "routed to balancing" from "rejected by a 400 guard".
	seedNodeWithStream(t, sm, seedNode{
		nodeID: "edge", host: "edge.example", active: true,
		ramMax: 100, ramCur: 10, cpu: 50,
	}, "live+show", 1, 0, 0)

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequestWithContext(context.Background(), "GET", "/live+show", nil)
	MistSourceHandler(c)

	if w.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", w.Code)
	}
}

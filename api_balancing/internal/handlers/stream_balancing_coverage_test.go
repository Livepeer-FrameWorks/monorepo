package handlers

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"frameworks/api_balancing/internal/control"
	"frameworks/api_balancing/internal/state"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	"github.com/gin-gonic/gin"
)

// handleStreamBalancing is Foghorn's core viewer-routing decision tree (the
// /<stream> endpoint). Each sub-test below drives one routing decision and
// asserts the ROUTING OUTCOME (status + body/Location), the real invariant
// that determines where a viewer is sent.
//
// Reachability note: the billing-suspended / 402-x402 branch is gated on
// target.TenantID != "", which is only populated when the concrete
// *commodore.GRPCClient (control.CommodoreClient) resolves the stream. That
// client is a concrete struct with no interface seam, so a non-empty TenantID
// cannot be injected here; the same constraint makes the fixed-node VOD branch
// (FixedNode is set by Commodore artifact resolution) unreachable from these
// stubs. Those branches are exercised via the resolver/x402 unit tests in
// internal/control and x402_helpers; see the campaign notes. With
// CommodoreClient nil, ResolveStream("live+...") returns a target with empty
// TenantID, which is exactly the dynamic-balancing (viewer playback) entry
// point this suite covers.

// balancingTestEnv wires the package globals handleStreamBalancing reads:
// a fresh in-memory balancer (lb) and a non-nil logger (the async
// postBalancingEvent goroutine dereferences it). decklogClient/metrics/
// triggerProcessor/remoteEdgeCache stay nil and are nil-guarded in the code.
func balancingTestEnv(t *testing.T) *state.StreamStateManager {
	t.Helper()
	sm := withSeededBalancer(t)
	// The async postBalancingEvent goroutine dereferences the package-global
	// `logger` and can outlive the test, so set it process-wide (do NOT restore
	// to nil on cleanup — a late goroutine would then panic).
	if logger == nil {
		logger = logging.NewLogger()
	}
	return sm
}

// seedOriginEdge makes nodeID/host a healthy active edge that is the ORIGIN
// for internalName (Inputs>0), which is what the balancer's source/origin
// check requires before it will select the node for viewer playback. The
// stream map is keyed by internal name (live+ prefix already stripped by
// handleStreamBalancing), so seed under the internal name, not the live+ form.
func seedOriginEdge(t *testing.T, sm *state.StreamStateManager, nodeID, host, internalName string) {
	t.Helper()
	seedNodeWithStream(t, sm, seedNode{
		nodeID: nodeID, host: host, active: true,
		ramMax: 100, ramCur: 10,
	}, internalName, 1, 0, 0)
}

// ginCtxFor builds a gin context whose request carries the given raw query so
// handleStreamBalancing can read proto=/cap=/lat= the way Mist sends them.
func ginCtxFor(t *testing.T, rawQuery string) (*gin.Context, *httptest.ResponseRecorder) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	target := "/stream"
	if rawQuery != "" {
		target += "?" + rawQuery
	}
	c.Request = httptest.NewRequestWithContext(context.Background(), "GET", target, nil)
	return c, w
}

// Viewer playback: a single healthy edge hosting the stream is selected and
// returned as the plain hostname (no proto => 200 body = host). This is the
// default load-balancer selection path.
func TestStreamBalancing_ViewerSelectsLocalEdge(t *testing.T) {
	sm := balancingTestEnv(t)
	seedOriginEdge(t, sm, "edge-a", "edge-a.example", "demo")

	c, w := ginCtxFor(t, "")
	handleStreamBalancing(c, "live+demo")

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	if w.Body.String() != "edge-a.example" {
		t.Fatalf("selected node = %q, want edge-a.example", w.Body.String())
	}
}

// Viewer playback with proto=: the selection becomes a 307 redirect to
// proto://host/<streamName>, preserving the original (non-internal) stream
// name in the redirect URL.
func TestStreamBalancing_ProtoRequestRedirectsToEdge(t *testing.T) {
	sm := balancingTestEnv(t)
	seedOriginEdge(t, sm, "edge-a", "edge-a.example", "demo")

	c, w := ginCtxFor(t, "proto=https")
	handleStreamBalancing(c, "live+demo")

	if w.Code != http.StatusTemporaryRedirect {
		t.Fatalf("status = %d, want 307", w.Code)
	}
	loc := w.Header().Get("Location")
	if !strings.HasPrefix(loc, "https://edge-a.example/live+demo") {
		t.Fatalf("Location = %q, want https://edge-a.example/live+demo...", loc)
	}
}

// Capability filter: cap=storage excludes an edge-only node, so no node
// matches and the C++-compatible "localhost" fallback is returned. This locks
// the invariant that the capability gate actually removes nodes from
// selection (vs. the unfiltered case which selects the edge).
func TestStreamBalancing_CapabilityFilterExcludesNonMatchingNode(t *testing.T) {
	sm := balancingTestEnv(t)
	// Edge-only node: CapStorage=false (seedNodeWithStream sets all caps false).
	seedOriginEdge(t, sm, "edge-a", "edge-a.example", "demo")

	// Same node, no cap filter -> selectable.
	c1, w1 := ginCtxFor(t, "")
	handleStreamBalancing(c1, "live+demo")
	if w1.Body.String() != "edge-a.example" {
		t.Fatalf("baseline (no cap) selected %q, want edge-a.example", w1.Body.String())
	}

	// With cap=storage the edge-only node is filtered out -> fallback.
	c2, w2 := ginCtxFor(t, "cap=storage")
	handleStreamBalancing(c2, "live+demo")
	if w2.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w2.Code)
	}
	if w2.Body.String() != "localhost" {
		t.Fatalf("cap=storage result = %q, want localhost fallback", w2.Body.String())
	}
}

// No nodes available and no cross-cluster replication arranged: the balancer
// errors and handleStreamBalancing emits the C++-compatible "localhost"
// fallback (200) rather than a hard failure. This is the fail-open invariant.
func TestStreamBalancing_NoNodesFallsBackToLocalhost(t *testing.T) {
	balancingTestEnv(t)
	// No nodes seeded.

	c, w := ginCtxFor(t, "")
	handleStreamBalancing(c, "live+ghost")

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	if w.Body.String() != "localhost" {
		t.Fatalf("body = %q, want localhost fallback", w.Body.String())
	}
}

// Cross-cluster origin-pull: when local selection fails but the stream
// registry shows this cluster is replicating the stream from a peer (a
// MarkReplicating arrangement with a PullDTSCURL), the balancer returns the
// remote DTSC source host instead of the localhost fallback. This is the
// cross_cluster_dtsc routing decision.
func TestStreamBalancing_CrossClusterReturnsRemoteDTSCHost(t *testing.T) {
	balancingTestEnv(t)

	prev := control.StreamRegistryInstance
	reg := control.NewStreamRegistry(nil, "cluster-local", time.Minute)
	control.StreamRegistryInstance = reg
	t.Cleanup(func() { control.StreamRegistryInstance = prev })

	// Arrange an origin-pull: cluster-local pulls "pulled" from peer-cluster
	// via a DTSC URL whose host is dtsc-origin.example:4200.
	reg.MarkReplicating(
		"live+pulled",
		"peer-cluster",
		"dtsc://dtsc-origin.example:4200/pulled",
		"dest-node", "https://dest-node.example", "src-node",
	)

	// No local nodes host this stream -> local selection fails -> DTSC branch.
	c, w := ginCtxFor(t, "")
	handleStreamBalancing(c, "live+pulled")

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	if w.Body.String() != "dtsc-origin.example:4200" {
		t.Fatalf("body = %q, want remote DTSC host dtsc-origin.example:4200", w.Body.String())
	}
}

// Cross-cluster replication is only consulted when local selection fails: if a
// healthy local edge hosts the stream, the local node wins even though a
// remote DTSC arrangement also exists. Locks selection precedence
// (local-first) over cross-cluster fallback.
func TestStreamBalancing_LocalEdgeWinsOverCrossClusterDTSC(t *testing.T) {
	sm := balancingTestEnv(t)

	prev := control.StreamRegistryInstance
	reg := control.NewStreamRegistry(nil, "cluster-local", time.Minute)
	control.StreamRegistryInstance = reg
	t.Cleanup(func() { control.StreamRegistryInstance = prev })
	reg.MarkReplicating(
		"live+both",
		"peer-cluster",
		"dtsc://dtsc-origin.example:4200/both",
		"dest-node", "https://dest-node.example", "src-node",
	)

	// A local edge that actually hosts the stream (origin under internal name).
	seedOriginEdge(t, sm, "local-edge", "local-edge.example", "both")

	c, w := ginCtxFor(t, "")
	handleStreamBalancing(c, "live+both")

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	if w.Body.String() != "local-edge.example" {
		t.Fatalf("body = %q, want local-edge.example (local-first beats DTSC)", w.Body.String())
	}
}

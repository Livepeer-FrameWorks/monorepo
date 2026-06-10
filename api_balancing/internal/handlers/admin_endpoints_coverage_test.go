package handlers

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"frameworks/api_balancing/internal/control"
	"frameworks/api_balancing/internal/state"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	quartermasterpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/quartermaster"
	"github.com/gin-gonic/gin"
)

// seedNodeWithCaps makes a node visible in the snapshot with explicit
// capability flags and roles. seedNodeWithStream's metrics struct leaves all
// caps false, but HandleNodesOverview's ?cap= filter reads exactly those
// fields, so the cap-filter tests must populate them directly.
func seedNodeWithCaps(t *testing.T, sm *state.StreamStateManager, nodeID, host string, active bool, caps struct {
	ingest, edge, storage, processing bool
}, roles []string, tags []string) {
	t.Helper()
	lat, lon := 0.0, 0.0
	sm.SetNodeInfo(nodeID, host, true, &lat, &lon, "loc", "", nil)
	sm.TouchNode(nodeID, true)
	sm.SetProbeVerified(nodeID, active)
	sm.UpdateNodeMetrics(nodeID, struct {
		CPU                  float64
		RAMMax               float64
		RAMCurrent           float64
		UpSpeed              float64
		DownSpeed            float64
		BWLimit              float64
		CapIngest            bool
		CapEdge              bool
		CapStorage           bool
		CapProcessing        bool
		Roles                []string
		StorageCapacityBytes uint64
		StorageUsedBytes     uint64
		ProcessingClasses    map[string]state.ClassCapacity
	}{
		RAMMax: 100, RAMCurrent: 50, UpSpeed: 1000, DownSpeed: 2000, BWLimit: 1_000_000,
		CapIngest: caps.ingest, CapEdge: caps.edge, CapStorage: caps.storage, CapProcessing: caps.processing,
		Roles: roles,
	})
	if tags != nil {
		sm.SetNodeConnectionInfo(context.Background(), nodeID, host, "tenant-1", "cluster-1", tags)
	}
}

// overviewContext builds a gin context whose request carries the given raw
// query string so HandleNodesOverview can read cap/offset/limit/full.
func overviewContext(rawQuery string) (*gin.Context, *httptest.ResponseRecorder) {
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequestWithContext(context.Background(), "GET", "/api/nodes?"+rawQuery, nil)
	return c, w
}

func decodeNodeList(t *testing.T, body []byte) []map[string]interface{} {
	t.Helper()
	var out []map[string]interface{}
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatalf("unmarshal node list %q: %v", string(body), err)
	}
	return out
}

func nodeIDSet(nodes []map[string]interface{}) map[string]bool {
	set := map[string]bool{}
	for _, n := range nodes {
		if id, ok := n["node_id"].(string); ok {
			set[id] = true
		}
	}
	return set
}

// HandleNodesOverview ?cap= filter: a node must satisfy EVERY requested cap
// (either via its Cap* flag or a matching role) to be returned. This is the
// core node-filtering invariant that drives operator/admin tooling.
func TestHandleNodesOverviewCapFilter(t *testing.T) {
	sm := withSeededBalancer(t)
	seedNodeWithCaps(t, sm, "edge-node", "edge.example", true,
		struct{ ingest, edge, storage, processing bool }{edge: true}, nil, nil)
	seedNodeWithCaps(t, sm, "storage-node", "storage.example", true,
		struct{ ingest, edge, storage, processing bool }{storage: true}, nil, nil)
	seedNodeWithCaps(t, sm, "both-node", "both.example", true,
		struct{ ingest, edge, storage, processing bool }{edge: true, storage: true}, nil, nil)

	// cap=storage must return exactly the two storage-capable nodes.
	c, w := overviewContext("cap=storage")
	HandleNodesOverview(c)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	got := nodeIDSet(decodeNodeList(t, w.Body.Bytes()))
	if len(got) != 2 || !got["storage-node"] || !got["both-node"] {
		t.Fatalf("cap=storage returned %v, want {storage-node, both-node}", got)
	}

	// cap=edge,storage requires BOTH on the same node → only both-node.
	c2, w2 := overviewContext("cap=edge,storage")
	HandleNodesOverview(c2)
	got2 := nodeIDSet(decodeNodeList(t, w2.Body.Bytes()))
	if len(got2) != 1 || !got2["both-node"] {
		t.Fatalf("cap=edge,storage returned %v, want {both-node}", got2)
	}
}

// A cap requested as neither a Cap* flag nor a role on any node yields the
// empty set — the filter rejects rather than falls through.
func TestHandleNodesOverviewCapFilterByRole(t *testing.T) {
	sm := withSeededBalancer(t)
	seedNodeWithCaps(t, sm, "gpu-node", "gpu.example", true,
		struct{ ingest, edge, storage, processing bool }{}, []string{"transcode"}, nil)
	seedNodeWithCaps(t, sm, "plain-node", "plain.example", true,
		struct{ ingest, edge, storage, processing bool }{edge: true}, nil, nil)

	c, w := overviewContext("cap=transcode")
	HandleNodesOverview(c)
	got := nodeIDSet(decodeNodeList(t, w.Body.Bytes()))
	if len(got) != 1 || !got["gpu-node"] {
		t.Fatalf("cap=transcode (role match) returned %v, want {gpu-node}", got)
	}

	c2, w2 := overviewContext("cap=nonexistent")
	HandleNodesOverview(c2)
	if got2 := decodeNodeList(t, w2.Body.Bytes()); len(got2) != 0 {
		t.Fatalf("cap=nonexistent returned %d nodes, want 0", len(got2))
	}
}

// offset/limit window the result slice; the returned subset and its size must
// honour the requested page boundaries.
func TestHandleNodesOverviewOffsetLimit(t *testing.T) {
	sm := withSeededBalancer(t)
	for _, id := range []string{"n1", "n2", "n3", "n4", "n5"} {
		seedNodeWithCaps(t, sm, id, id+".example", true,
			struct{ ingest, edge, storage, processing bool }{edge: true}, nil, nil)
	}

	// limit=2 returns at most 2 nodes.
	c, w := overviewContext("limit=2")
	HandleNodesOverview(c)
	if got := decodeNodeList(t, w.Body.Bytes()); len(got) != 2 {
		t.Fatalf("limit=2 returned %d nodes, want 2", len(got))
	}

	// offset past the end clamps to empty, never panics or wraps.
	c2, w2 := overviewContext("offset=100&limit=10")
	HandleNodesOverview(c2)
	if got := decodeNodeList(t, w2.Body.Bytes()); len(got) != 0 {
		t.Fatalf("offset=100 returned %d nodes, want 0", len(got))
	}

	// No limit returns the full set (5 seeded nodes).
	c3, w3 := overviewContext("")
	HandleNodesOverview(c3)
	if got := decodeNodeList(t, w3.Body.Bytes()); len(got) != 5 {
		t.Fatalf("unbounded query returned %d nodes, want 5", len(got))
	}
}

// HandleSetNodeMaintenanceMode persists the requested mode in the state manager
// and the response echoes the now-current mode. This is the drain/maintenance
// state-transition invariant. control.Init installs a (connectionless) registry
// so the best-effort PushOperationalMode returns ErrNotConnected instead of
// dereferencing a nil global.
func TestHandleSetNodeMaintenanceModePersists(t *testing.T) {
	sm := withSeededBalancer(t)
	seedNodeWithCaps(t, sm, "node-A", "a.example", true,
		struct{ ingest, edge, storage, processing bool }{edge: true}, nil, nil)

	control.Init(logging.NewLogger(), nil, nil)
	prevLogger := logger
	logger = logging.NewLogger()
	t.Cleanup(func() { logger = prevLogger })

	c, w := newGinTestContext()
	c.Params = gin.Params{{Key: "node_id", Value: "node-A"}}
	c.Request = httptest.NewRequestWithContext(context.Background(), "POST", "/maintenance",
		strings.NewReader(`{"mode":"maintenance","set_by":"operator"}`))

	HandleSetNodeMaintenanceMode(c)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", w.Code, w.Body.String())
	}
	var resp map[string]interface{}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if resp["operational_mode"] != string(state.NodeModeMaintenance) {
		t.Fatalf("response mode = %v, want maintenance", resp["operational_mode"])
	}
	// The transition must be durable in the state manager, not just echoed.
	if got := sm.GetNodeOperationalMode("node-A"); got != state.NodeModeMaintenance {
		t.Fatalf("persisted mode = %q, want maintenance", got)
	}
}

// Missing node_id and malformed JSON are both rejected with 400 before any
// state mutation, and an unknown/empty operational mode value is rejected by
// normalization.
func TestHandleSetNodeMaintenanceModeRejections(t *testing.T) {
	withSeededBalancer(t)
	control.Init(logging.NewLogger(), nil, nil)

	// Empty node_id → 400.
	c, w := newGinTestContext()
	c.Request = httptest.NewRequestWithContext(context.Background(), "POST", "/maintenance",
		strings.NewReader(`{"mode":"maintenance"}`))
	HandleSetNodeMaintenanceMode(c)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("missing node_id status = %d, want 400", w.Code)
	}

	// Malformed JSON body → 400.
	c2, w2 := newGinTestContext()
	c2.Params = gin.Params{{Key: "node_id", Value: "node-A"}}
	c2.Request = httptest.NewRequestWithContext(context.Background(), "POST", "/maintenance",
		strings.NewReader(`{not json`))
	HandleSetNodeMaintenanceMode(c2)
	if w2.Code != http.StatusBadRequest {
		t.Fatalf("bad JSON status = %d, want 400", w2.Code)
	}

	// Unknown mode value → 400 from normalizeNodeOperationalMode.
	c3, w3 := newGinTestContext()
	c3.Params = gin.Params{{Key: "node_id", Value: "node-A"}}
	c3.Request = httptest.NewRequestWithContext(context.Background(), "POST", "/maintenance",
		strings.NewReader(`{"mode":"bogus"}`))
	HandleSetNodeMaintenanceMode(c3)
	if w3.Code != http.StatusBadRequest {
		t.Fatalf("bad mode status = %d, want 400", w3.Code)
	}
}

// HandleGetNodeDrainStatus returns the live mode/active-viewer snapshot for a
// known node, and 404 for an unknown one (existence is gated on GetNodeState).
func TestHandleGetNodeDrainStatus(t *testing.T) {
	sm := withSeededBalancer(t)
	seedNodeWithCaps(t, sm, "node-D", "d.example", true,
		struct{ ingest, edge, storage, processing bool }{edge: true}, nil, nil)
	control.Init(logging.NewLogger(), nil, nil)
	if err := sm.SetNodeOperationalMode(context.Background(), "node-D", state.NodeModeDraining, "op"); err != nil {
		t.Fatalf("set draining: %v", err)
	}

	c, w := newGinTestContext()
	c.Params = gin.Params{{Key: "node_id", Value: "node-D"}}
	HandleGetNodeDrainStatus(c)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", w.Code, w.Body.String())
	}
	var resp map[string]interface{}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if resp["operational_mode"] != string(state.NodeModeDraining) {
		t.Fatalf("mode = %v, want draining", resp["operational_mode"])
	}

	// Unknown node → 404.
	c2, w2 := newGinTestContext()
	c2.Params = gin.Params{{Key: "node_id", Value: "ghost"}}
	HandleGetNodeDrainStatus(c2)
	if w2.Code != http.StatusNotFound {
		t.Fatalf("unknown node status = %d, want 404", w2.Code)
	}
}

// handleListServers reports each host's MistServer-compat status string. The
// online-vs-offline categorization is the contract: probe-verified (active)
// nodes are "Monitored (online)", everything else "Offline".
func TestHandleListServersOnlineOfflineCategorization(t *testing.T) {
	sm := withSeededBalancer(t)
	seedNodeWithCaps(t, sm, "online-node", "online.example", true,
		struct{ ingest, edge, storage, processing bool }{edge: true}, nil, nil)
	seedNodeWithCaps(t, sm, "offline-node", "offline.example", false,
		struct{ ingest, edge, storage, processing bool }{edge: true}, nil, nil)

	c, w := newGinTestContext()
	handleListServers(c)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	var result map[string]string
	if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
		t.Fatalf("unmarshal %q: %v", w.Body.String(), err)
	}
	if result["online.example"] != "Monitored (online)" {
		t.Fatalf("online host status = %q, want Monitored (online)", result["online.example"])
	}
	if result["offline.example"] != "Offline" {
		t.Fatalf("offline host status = %q, want Offline", result["offline.example"])
	}
}

// ApplyBootstrapMetadata hydrates the cluster-attribution globals (clusterID
// and ownerTenantID) used by dual-tenant routing-event emission. Globals are
// saved/restored so the test is isolated.
func TestApplyBootstrapMetadataHydratesAttribution(t *testing.T) {
	prevCluster, prevOwner := clusterID, ownerTenantID
	t.Cleanup(func() { clusterID, ownerTenantID = prevCluster, prevOwner })
	clusterID, ownerTenantID = "", ""

	owner := "tenant-owner-123"
	resp := &quartermasterpb.BootstrapServiceResponse{
		ClusterId:     "cluster-xyz",
		OwnerTenantId: &owner,
	}
	ApplyBootstrapMetadata(resp)

	gotCluster, gotOwner := GetClusterInfo()
	if gotCluster != "cluster-xyz" {
		t.Fatalf("clusterID = %q, want cluster-xyz", gotCluster)
	}
	if gotOwner != "tenant-owner-123" {
		t.Fatalf("ownerTenantID = %q, want tenant-owner-123", gotOwner)
	}
}

// Nil and empty-field bootstrap responses must leave the attribution globals
// untouched (no spurious overwrite to empty).
func TestApplyBootstrapMetadataNilAndEmptyAreNoops(t *testing.T) {
	prevCluster, prevOwner := clusterID, ownerTenantID
	t.Cleanup(func() { clusterID, ownerTenantID = prevCluster, prevOwner })
	clusterID, ownerTenantID = "existing-cluster", "existing-owner"

	ApplyBootstrapMetadata(nil)
	if c, o := GetClusterInfo(); c != "existing-cluster" || o != "existing-owner" {
		t.Fatalf("nil input mutated globals: cluster=%q owner=%q", c, o)
	}

	// Empty owner string and clusterID already set → both left as-is.
	empty := ""
	ApplyBootstrapMetadata(&quartermasterpb.BootstrapServiceResponse{
		ClusterId:     "different-cluster",
		OwnerTenantId: &empty,
	})
	if c, o := GetClusterInfo(); c != "existing-cluster" || o != "existing-owner" {
		t.Fatalf("empty-field input mutated globals: cluster=%q owner=%q", c, o)
	}
}

// MistServerCompatibilityHandler dispatches purely on method/path before any
// client call. These branches lock the URI routing contract: HTTP/2 PRI,
// favicon, /source/by-node/ without a source param, and invalid stream names.
func TestMistServerCompatibilityHandlerDispatch(t *testing.T) {
	gin.SetMode(gin.TestMode)

	t.Run("PRI preface returns 200 empty", func(t *testing.T) {
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request = httptest.NewRequestWithContext(context.Background(), "PRI", "/", nil)
		c.Request.RequestURI = "*"
		MistServerCompatibilityHandler(c)
		if w.Code != http.StatusOK || w.Body.String() != "" {
			t.Fatalf("PRI dispatch = %d %q, want 200 empty", w.Code, w.Body.String())
		}
	})

	t.Run("favicon returns 404", func(t *testing.T) {
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request = httptest.NewRequestWithContext(context.Background(), "GET", "/favicon.ico", nil)
		MistServerCompatibilityHandler(c)
		if w.Code != http.StatusNotFound {
			t.Fatalf("favicon dispatch = %d, want 404", w.Code)
		}
	})

	t.Run("source-by-node without source param is 400", func(t *testing.T) {
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request = httptest.NewRequestWithContext(context.Background(), "GET", sourceByNodePathPrefix, nil)
		MistServerCompatibilityHandler(c)
		if w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), "Missing source") {
			t.Fatalf("source-by-node dispatch = %d %q, want 400 Missing source", w.Code, w.Body.String())
		}
	})

	t.Run("invalid stream name is 400", func(t *testing.T) {
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		// A single-char name fails StreamIDRegex (min length 4).
		c.Request = httptest.NewRequestWithContext(context.Background(), "GET", "/x", nil)
		MistServerCompatibilityHandler(c)
		if w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), "Invalid stream name") {
			t.Fatalf("invalid stream dispatch = %d %q, want 400 Invalid stream name", w.Code, w.Body.String())
		}
	})
}

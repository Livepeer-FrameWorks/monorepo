package handlers

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"testing"

	"frameworks/api_balancing/internal/balancer"
	"frameworks/api_balancing/internal/state"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	"github.com/gin-gonic/gin"
)

// withSeededBalancer resets the shared state manager, installs a fresh in-memory
// LoadBalancer as the package-global `lb`, and restores the previous value on
// cleanup. The compat handlers (/?viewers=, /?host=, etc.) read exclusively
// through `lb`, which projects from state.DefaultManager().
func withSeededBalancer(t *testing.T) *state.StreamStateManager {
	t.Helper()
	gin.SetMode(gin.TestMode)
	sm := state.ResetDefaultManagerForTests()
	t.Cleanup(sm.Shutdown)

	prev := lb
	lb = balancer.NewLoadBalancer(logging.NewLogger())
	t.Cleanup(func() { lb = prev })
	return sm
}

type seedNode struct {
	nodeID   string
	host     string
	active   bool // false => healthy but not probe-verified (in snapshot, IsActive=false)
	lat, lon float64
	tags     []string
	ramMax   float64
	ramCur   float64
	cpu      float64
}

// seed makes a node visible in the balancer snapshot. A node must be healthy
// and non-stale to appear at all; IsActive additionally requires probe
// verification, so an inactive-but-present node is healthy with probe=false.
func seedNodeWithStream(t *testing.T, sm *state.StreamStateManager, n seedNode, stream string, viewers int, bytesUp, bytesDown int64) {
	t.Helper()
	lat, lon := n.lat, n.lon
	sm.SetNodeInfo(n.nodeID, n.host, true, &lat, &lon, "loc", "", nil)
	sm.TouchNode(n.nodeID, true)
	sm.SetProbeVerified(n.nodeID, n.active)
	sm.UpdateNodeMetrics(n.nodeID, struct {
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
	}{CPU: n.cpu, RAMMax: n.ramMax, RAMCurrent: n.ramCur, UpSpeed: 1000, DownSpeed: 2000, BWLimit: 1_000_000})
	if len(n.tags) > 0 {
		sm.SetNodeConnectionInfo(context.Background(), n.nodeID, n.host, "tenant-1", "cluster-1", n.tags)
	}
	if stream != "" {
		sm.UpdateNodeStats(stream, n.nodeID, viewers, 1, bytesUp, bytesDown, false)
		if viewers > 0 {
			sm.UpdateUserConnection(stream, n.nodeID, "tenant-1", viewers)
		}
	}
}

func newGinTestContext() (*gin.Context, *httptest.ResponseRecorder) {
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequestWithContext(context.Background(), "GET", "/", nil)
	return c, w
}

// handleViewerCount sums viewers across active nodes hosting the stream;
// non-active nodes (present in the snapshot but not probe-verified) and nodes
// not hosting the stream contribute nothing.
func TestHandleViewerCount(t *testing.T) {
	sm := withSeededBalancer(t)
	seedNodeWithStream(t, sm, seedNode{nodeID: "a", host: "a.example", active: true, ramMax: 100, ramCur: 50}, "live+demo", 3, 0, 0)
	seedNodeWithStream(t, sm, seedNode{nodeID: "b", host: "b.example", active: true, ramMax: 100, ramCur: 50}, "live+demo", 2, 0, 0)
	seedNodeWithStream(t, sm, seedNode{nodeID: "c", host: "c.example", active: false, ramMax: 100, ramCur: 50}, "live+demo", 10, 0, 0)

	c, w := newGinTestContext()
	handleViewerCount(c, "live+demo")

	if w.Body.String() != "5" {
		t.Fatalf("viewer count = %q, want 5 (inactive node excluded)", w.Body.String())
	}
}

func TestHandleViewerCountMissingStreamIsZero(t *testing.T) {
	sm := withSeededBalancer(t)
	seedNodeWithStream(t, sm, seedNode{nodeID: "a", host: "a.example", active: true, ramMax: 100, ramCur: 50}, "live+demo", 3, 0, 0)

	c, w := newGinTestContext()
	handleViewerCount(c, "live+other")

	if w.Body.String() != "0" {
		t.Fatalf("viewer count = %q, want 0", w.Body.String())
	}
}

// handleStreamStats returns the [viewers, bandwidth, bytesUp, bytesDown] tuple
// for the first active node carrying the stream.
func TestHandleStreamStats(t *testing.T) {
	sm := withSeededBalancer(t)
	seedNodeWithStream(t, sm, seedNode{nodeID: "a", host: "a.example", active: true, ramMax: 100, ramCur: 50}, "live+demo", 3, 1000, 2000)

	c, w := newGinTestContext()
	handleStreamStats(c, "live+demo")

	var result map[string][]float64
	if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
		t.Fatalf("unmarshal %q: %v", w.Body.String(), err)
	}
	tuple, ok := result["live+demo"]
	if !ok {
		t.Fatalf("missing stream key in %v", result)
	}
	if len(tuple) != 4 || tuple[0] != 3 || tuple[2] != 1000 || tuple[3] != 2000 {
		t.Fatalf("tuple = %v, want [3 _ 1000 2000]", tuple)
	}
}

func TestHandleStreamStatsAbsentStreamEmpty(t *testing.T) {
	sm := withSeededBalancer(t)
	seedNodeWithStream(t, sm, seedNode{nodeID: "a", host: "a.example", active: true, ramMax: 100, ramCur: 50}, "live+demo", 3, 1000, 2000)

	c, w := newGinTestContext()
	handleStreamStats(c, "live+missing")

	var result map[string][]float64
	if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
		t.Fatalf("unmarshal %q: %v", w.Body.String(), err)
	}
	if len(result) != 0 {
		t.Fatalf("result = %v, want empty", result)
	}
}

// handleHostStatus emits a per-host status map; the geo and tags sub-objects
// appear only when set, and CPU/RAM are rescaled to percentages.
func TestHandleHostStatusSingleHost(t *testing.T) {
	sm := withSeededBalancer(t)
	seedNodeWithStream(t, sm, seedNode{
		nodeID: "a", host: "a.example", active: true,
		lat: 52.1, lon: 4.3, tags: []string{"edge", "eu"},
		ramMax: 100, ramCur: 50, cpu: 200,
	}, "live+demo", 4, 0, 0)

	c, w := newGinTestContext()
	handleHostStatus(c, "a.example")

	var result map[string]map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
		t.Fatalf("unmarshal %q: %v", w.Body.String(), err)
	}
	host, ok := result["a.example"]
	if !ok {
		t.Fatalf("missing host key in %v", result)
	}
	if host["cpu"].(float64) != 20 { // 200/10
		t.Fatalf("cpu = %v, want 20", host["cpu"])
	}
	if host["ram"].(float64) != 50 { // 50*100/100
		t.Fatalf("ram = %v, want 50", host["ram"])
	}
	if host["viewers"].(float64) != 4 {
		t.Fatalf("viewers = %v, want 4", host["viewers"])
	}
	if _, ok := host["geo"]; !ok {
		t.Fatalf("geo block missing despite lat/lon set: %v", host)
	}
	if _, ok := host["tags"]; !ok {
		t.Fatalf("tags block missing despite tags set: %v", host)
	}
}

func TestHandleHostStatusFilterExcludesOtherHosts(t *testing.T) {
	sm := withSeededBalancer(t)
	seedNodeWithStream(t, sm, seedNode{nodeID: "a", host: "a.example", active: true, ramMax: 100, ramCur: 50}, "", 0, 0, 0)
	seedNodeWithStream(t, sm, seedNode{nodeID: "b", host: "b.example", active: true, ramMax: 100, ramCur: 50}, "", 0, 0, 0)

	c, w := newGinTestContext()
	handleHostStatus(c, "a.example")

	var result map[string]map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
		t.Fatalf("unmarshal %q: %v", w.Body.String(), err)
	}
	if len(result) != 1 {
		t.Fatalf("expected only the filtered host, got %v", result)
	}
	if _, ok := result["a.example"]; !ok {
		t.Fatalf("filtered host missing: %v", result)
	}
}

// handleWeights returns the current scoring weights, and applies any provided
// weight overrides before echoing them back.
func TestHandleWeights(t *testing.T) {
	withSeededBalancer(t)

	c, w := newGinTestContext()
	handleWeights(c, "")
	var base map[string]uint64
	if err := json.Unmarshal(w.Body.Bytes(), &base); err != nil {
		t.Fatalf("unmarshal %q: %v", w.Body.String(), err)
	}
	for _, k := range []string{"cpu", "ram", "bw", "geo", "bonus"} {
		if _, ok := base[k]; !ok {
			t.Fatalf("weights response missing %q: %v", k, base)
		}
	}

	c2, w2 := newGinTestContext()
	handleWeights(c2, `{"cpu":99}`)
	var updated map[string]uint64
	if err := json.Unmarshal(w2.Body.Bytes(), &updated); err != nil {
		t.Fatalf("unmarshal %q: %v", w2.Body.String(), err)
	}
	if updated["cpu"] != 99 {
		t.Fatalf("cpu weight = %d, want 99", updated["cpu"])
	}
}

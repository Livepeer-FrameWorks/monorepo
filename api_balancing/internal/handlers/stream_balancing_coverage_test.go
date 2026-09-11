package handlers

import (
	"context"
	"net/http/httptest"
	"testing"

	"frameworks/api_balancing/internal/control"
	"frameworks/api_balancing/internal/state"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	"github.com/gin-gonic/gin"
)

// balancingTestEnv supplies a fresh inventory and logger for source and
// playback handler tests.
func balancingTestEnv(t *testing.T) *state.StreamStateManager {
	t.Helper()
	control.SetLocalClusterID("test-platform-cluster")
	control.AddPlatformSharedCluster("test-platform-cluster")
	sm := withSeededBalancer(t)
	lb.SetClusterServeAuthorizer(control.ClusterServeAccessibleForScope)
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
// stream map is keyed by the unprefixed internal name.
func seedOriginEdge(t *testing.T, sm *state.StreamStateManager, nodeID, host, internalName string) {
	t.Helper()
	seedNodeWithStream(t, sm, seedNode{
		nodeID: nodeID, host: host, active: true,
		ramMax: 100, ramCur: 10,
	}, internalName, 1, 0, 0)
	sm.SetNodeConnectionInfo(context.Background(), nodeID, host, "", "test-platform-cluster", nil)
}

// ginCtxFor builds a request context with the supplied raw query.
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

package triggers

import (
	"testing"

	"frameworks/api_balancing/internal/ingesterrors"
	"frameworks/api_balancing/internal/state"
	ipcpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/ipc"
)

func TestTenantViewerCapBlocksNewViewerWhenAtCap(t *testing.T) {
	tc := state.ResetDefaultTenantCapacityForTests()
	const tenantID = "t-vcap"
	if allowed, _, _, err := tc.TryRegisterViewer(tenantID, "node-a", "session-a", "viewer-a", 2); err != nil || !allowed {
		t.Fatalf("reserve viewer-a: allowed=%v err=%v", allowed, err)
	}
	if allowed, _, _, err := tc.TryRegisterViewer(tenantID, "node-b", "session-b", "viewer-b", 2); err != nil || !allowed {
		t.Fatalf("reserve viewer-b: allowed=%v err=%v", allowed, err)
	}

	allowed, added, count, err := tc.TryRegisterViewer(tenantID, "node-c", "session-c", "viewer-c", 2)
	if err != nil || allowed || added || count != 2 {
		t.Fatalf("over-cap viewer reservation = allowed=%v added=%v count=%d err=%v", allowed, added, count, err)
	}
}

func TestTenantViewerCapDeduplicatesCapacityIDAcrossMistSessions(t *testing.T) {
	tc := state.ResetDefaultTenantCapacityForTests()
	const tenantID = "t-vrefire"
	if allowed, _, _, err := tc.TryRegisterViewer(tenantID, "node-a", "session-a", "fwcid-a", 1); err != nil || !allowed {
		t.Fatalf("first session: allowed=%v err=%v", allowed, err)
	}
	allowed, added, count, err := tc.TryRegisterViewer(tenantID, "node-b", "session-b", "fwcid-a", 1)
	if err != nil || !allowed || !added || count != 1 {
		t.Fatalf("same logical viewer on another session = allowed=%v added=%v count=%d err=%v", allowed, added, count, err)
	}
	if _, released, count, err := tc.ReleaseViewerSession(tenantID, "node-a", "session-a"); err != nil || !released || count != 1 {
		t.Fatalf("release first session = released=%v count=%d err=%v", released, count, err)
	}
	if _, released, count, err := tc.ReleaseViewerSession(tenantID, "node-b", "session-b"); err != nil || !released || count != 0 {
		t.Fatalf("release final session = released=%v count=%d err=%v", released, count, err)
	}
}

func TestIngestErrorCodeForTenantStreamCap(t *testing.T) {
	err := ingesterrors.New(
		ipcpb.IngestErrorCode_INGEST_ERROR_TENANT_STREAM_CAP,
		"concurrent stream cap reached (3/3) — close another stream or upgrade",
	)
	if err == nil {
		t.Fatal("expected non-nil error")
	}
	if err.Code != ipcpb.IngestErrorCode_INGEST_ERROR_TENANT_STREAM_CAP {
		t.Errorf("error code: got %v want INGEST_ERROR_TENANT_STREAM_CAP", err.Code)
	}
}

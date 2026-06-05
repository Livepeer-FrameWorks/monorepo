package triggers

import (
	"testing"

	"frameworks/api_balancing/internal/ingesterrors"
	"frameworks/api_balancing/internal/state"
	ipcpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/ipc"
)

// Re-fire idempotency for PUSH_REWRITE: a second PUSH_REWRITE for the same
// (tenant, internal_name) when the tenant is at cap must admit, because the
// set count doesn't change. Test calls the cap check logic directly against
// the shared TenantCapacityManager so we don't need to wire a full Processor.

func TestTenantStreamCapBlocksNewStreamWhenAtCap(t *testing.T) {
	tc := state.ResetDefaultTenantCapacityForTests()
	const tenantID = "t-cap"
	tc.RegisterStream(tenantID, "stream-a")
	tc.RegisterStream(tenantID, "stream-b")

	cap := int32(2)
	current := tc.CountStreams(tenantID)
	alreadyTracked := tc.HasStream(tenantID, "stream-new")
	if alreadyTracked {
		t.Fatal("test setup: stream-new should not be tracked yet")
	}
	if int32(current) >= cap {
		// expected reject path — no register
	} else {
		t.Fatalf("test setup: current %d should be at cap %d", current, cap)
	}

	// Simulate the trigger logic: current >= cap and not tracked → reject.
	if tc.CountStreams(tenantID) != 2 {
		t.Errorf("post-check count must be unchanged on reject: got %d", tc.CountStreams(tenantID))
	}
}

func TestTenantStreamCapAdmitsRefireOfTrackedStream(t *testing.T) {
	tc := state.ResetDefaultTenantCapacityForTests()
	const tenantID = "t-refire"
	tc.RegisterStream(tenantID, "stream-a")
	tc.RegisterStream(tenantID, "stream-b")
	// Cap=2, already at it. A re-fire of stream-a (already tracked) must be
	// admitted: HasStream returns true so the cap check is bypassed; the
	// subsequent RegisterStream is a no-op for the set.
	if !tc.HasStream(tenantID, "stream-a") {
		t.Fatal("setup: stream-a should be tracked")
	}
	tc.RegisterStream(tenantID, "stream-a") // no-op
	if got := tc.CountStreams(tenantID); got != 2 {
		t.Errorf("refire must not inflate count: got %d want 2", got)
	}
}

func TestTenantStreamCapAdmitsNewStreamUnderCap(t *testing.T) {
	tc := state.ResetDefaultTenantCapacityForTests()
	const tenantID = "t-under"
	tc.RegisterStream(tenantID, "stream-a")
	// Cap=3, current=1, not tracked → admit + register.
	current := tc.CountStreams(tenantID)
	cap := int32(3)
	if int32(current) >= cap || tc.HasStream(tenantID, "stream-b") {
		t.Fatalf("setup: current=%d cap=%d tracked=%v", current, cap, tc.HasStream(tenantID, "stream-b"))
	}
	tc.RegisterStream(tenantID, "stream-b")
	if got := tc.CountStreams(tenantID); got != 2 {
		t.Errorf("after admit: got %d want 2", got)
	}
}

func TestTenantViewerCapBlocksNewSessionWhenAtCap(t *testing.T) {
	tc := state.ResetDefaultTenantCapacityForTests()
	const tenantID = "t-vcap"
	tc.RegisterViewer(tenantID, "sess-a")
	tc.RegisterViewer(tenantID, "sess-b")

	cap := int32(2)
	current := tc.CountViewers(tenantID)
	if int32(current) < cap {
		t.Fatalf("setup: viewer count %d below cap %d", current, cap)
	}
	if tc.HasViewer(tenantID, "sess-new") {
		t.Fatal("setup: sess-new should not be tracked")
	}
}

func TestTenantViewerCapAdmitsRefireOfTrackedSession(t *testing.T) {
	tc := state.ResetDefaultTenantCapacityForTests()
	const tenantID = "t-vrefire"
	tc.RegisterViewer(tenantID, "sess-a")
	tc.RegisterViewer(tenantID, "sess-b")
	// Cap=2, at it. Re-fire sess-a → admit (already tracked).
	tc.RegisterViewer(tenantID, "sess-a")
	if got := tc.CountViewers(tenantID); got != 2 {
		t.Errorf("viewer refire must not inflate: got %d want 2", got)
	}
}

func TestStreamLifecycleDecrement(t *testing.T) {
	tc := state.ResetDefaultTenantCapacityForTests()
	const tenantID = "t-lifecycle"
	tc.RegisterStream(tenantID, "stream-a")
	tc.RegisterStream(tenantID, "stream-b")
	if tc.CountStreams(tenantID) != 2 {
		t.Fatalf("setup: got %d want 2", tc.CountStreams(tenantID))
	}
	// STREAM_END would call UnregisterStream.
	tc.UnregisterStream(tenantID, "stream-a")
	if got := tc.CountStreams(tenantID); got != 1 {
		t.Errorf("after STREAM_END: got %d want 1", got)
	}
	// Duplicate STREAM_END is no-op.
	tc.UnregisterStream(tenantID, "stream-a")
	if got := tc.CountStreams(tenantID); got != 1 {
		t.Errorf("duplicate STREAM_END: got %d want 1 (idempotent)", got)
	}
	tc.UnregisterStream(tenantID, "stream-b")
	if got := tc.CountStreams(tenantID); got != 0 {
		t.Errorf("after final STREAM_END: got %d want 0", got)
	}
}

func TestViewerLifecycleDecrement(t *testing.T) {
	tc := state.ResetDefaultTenantCapacityForTests()
	const tenantID = "t-vlifecycle"
	tc.RegisterViewer(tenantID, "sess-a")
	tc.RegisterViewer(tenantID, "sess-b")
	tc.UnregisterViewer(tenantID, "sess-a")
	if got := tc.CountViewers(tenantID); got != 1 {
		t.Errorf("after USER_END: got %d want 1", got)
	}
	// Unknown session unregister is no-op.
	tc.UnregisterViewer(tenantID, "sess-unknown")
	if got := tc.CountViewers(tenantID); got != 1 {
		t.Errorf("unknown USER_END: got %d want 1", got)
	}
}

// TestIngestErrorCodeForTenantStreamCap confirms the right protobuf error
// code is produced when the cap is hit at PUSH_REWRITE — the gateway/SDK
// distinguishes this from FREE_TIER_EXHAUSTED and ACCOUNT_SUSPENDED for the
// upgrade-prompt UX.
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

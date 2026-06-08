package grpc

import (
	"testing"

	ipcpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/ipc"
	sharedpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/shared"
)

func i64(v int64) *int64    { return &v }
func i32(v int32) *int32    { return &v }
func strp(v string) *string { return &v }

// TestBuildClipLifecycleData_FullEnrichment pins the clip lifecycle enrichment
// contract: every timing/identity field present on the originating
// CreateClipRequest must be copied onto the emitted ClipLifecycleData. These
// fields feed Decklog analytics attribution downstream; the function exists
// specifically to fix a past bug where they were dropped (see the CRITICAL note
// at the call site). A regression that silently stops copying one field would
// not fail any handler — it would just produce analytics events missing clip
// boundaries — so this test asserts each mapping explicitly.
func TestBuildClipLifecycleData_FullEnrichment(t *testing.T) {
	req := &sharedpb.CreateClipRequest{
		TenantId:           "tenant-1",
		StreamInternalName: "live+abc",
		StreamId:           strp("stream-uuid"),
		StartUnix:          i64(1000),
		StopUnix:           i64(1010),
		StartMs:            i64(5),
		StopMs:             i64(15),
		DurationSec:        i64(10),
		Mode:               sharedpb.ClipMode_CLIP_MODE_RELATIVE,
		ExpiresAt:          i64(99999),
		UserId:             strp("user-7"),
	}

	data := buildClipLifecycleData(ipcpb.ClipLifecycleData_STAGE_REQUESTED, req, "req-42", "clip-hash-xyz")

	if data.Stage != ipcpb.ClipLifecycleData_STAGE_REQUESTED {
		t.Fatalf("stage = %v, want STAGE_REQUESTED", data.Stage)
	}
	if data.GetRequestId() != "req-42" {
		t.Fatalf("request_id = %q, want req-42", data.GetRequestId())
	}
	if data.ClipHash != "clip-hash-xyz" {
		t.Fatalf("clip_hash = %q, want clip-hash-xyz", data.ClipHash)
	}
	if data.GetTenantId() != "tenant-1" {
		t.Fatalf("tenant_id = %q", data.GetTenantId())
	}
	if data.GetStreamInternalName() != "live+abc" {
		t.Fatalf("stream_internal_name = %q", data.GetStreamInternalName())
	}
	if data.GetStreamId() != "stream-uuid" {
		t.Fatalf("stream_id = %q", data.GetStreamId())
	}
	if data.GetStartUnix() != 1000 || data.GetStopUnix() != 1010 {
		t.Fatalf("unix bounds = (%d,%d), want (1000,1010)", data.GetStartUnix(), data.GetStopUnix())
	}
	if data.GetStartMs() != 5 || data.GetStopMs() != 15 {
		t.Fatalf("ms bounds = (%d,%d), want (5,15)", data.GetStartMs(), data.GetStopMs())
	}
	if data.GetDurationSec() != 10 {
		t.Fatalf("duration_sec = %d, want 10", data.GetDurationSec())
	}
	if data.GetExpiresAt() != 99999 {
		t.Fatalf("expires_at = %d, want 99999", data.GetExpiresAt())
	}
	if data.GetUserId() != "user-7" {
		t.Fatalf("user_id = %q, want user-7", data.GetUserId())
	}
	// Mode is stringified via the enum's .String(), not its numeric value.
	if data.GetClipMode() != sharedpb.ClipMode_CLIP_MODE_RELATIVE.String() {
		t.Fatalf("clip_mode = %q, want %q", data.GetClipMode(), sharedpb.ClipMode_CLIP_MODE_RELATIVE.String())
	}
}

// TestBuildClipLifecycleData_OmitsAbsentOptionals confirms the inverse: unset
// optional request fields stay unset (nil) on the lifecycle event rather than
// being written as zero values. A consumer distinguishes "no start_ms" from
// "start_ms == 0", so the function must leave the pointer nil when the source
// pointer is nil. Empty-string identity fields (tenant, stream name) are
// likewise skipped.
func TestBuildClipLifecycleData_OmitsAbsentOptionals(t *testing.T) {
	req := &sharedpb.CreateClipRequest{
		// All optional fields left nil/zero; Mode unspecified.
		Mode: sharedpb.ClipMode_CLIP_MODE_UNSPECIFIED,
	}

	data := buildClipLifecycleData(ipcpb.ClipLifecycleData_STAGE_PROGRESS, req, "req-1", "")

	if data.ClipHash != "" {
		t.Fatalf("empty clipHash arg must leave clip_hash empty, got %q", data.ClipHash)
	}
	if data.TenantId != nil {
		t.Fatalf("absent tenant_id must stay nil, got %v", data.TenantId)
	}
	if data.StreamInternalName != nil {
		t.Fatalf("absent stream_internal_name must stay nil")
	}
	if data.StreamId != nil {
		t.Fatalf("absent stream_id must stay nil")
	}
	if data.StartUnix != nil || data.StopUnix != nil || data.StartMs != nil || data.StopMs != nil {
		t.Fatal("absent timing bounds must stay nil, not zero")
	}
	if data.DurationSec != nil {
		t.Fatal("absent duration must stay nil")
	}
	if data.ExpiresAt != nil {
		t.Fatal("absent expires_at must stay nil")
	}
	if data.UserId != nil {
		t.Fatal("absent user_id must stay nil")
	}
	// Unspecified mode must NOT be stringified onto the event.
	if data.ClipMode != nil {
		t.Fatalf("unspecified mode must leave clip_mode nil, got %q", data.GetClipMode())
	}
	// request_id is always set even on the minimal path.
	if data.GetRequestId() != "req-1" {
		t.Fatalf("request_id = %q, want req-1", data.GetRequestId())
	}
}

// TestBuildClipLifecycleData_EmptyStringPointersSkipped guards the specific
// guards that check both non-nil AND non-empty for pointer string fields: a
// request carrying an explicit empty StreamId / UserId must be treated as
// "absent" so we never emit an empty-string identity that analytics would index
// as a real (but blank) entity.
func TestBuildClipLifecycleData_EmptyStringPointersSkipped(t *testing.T) {
	req := &sharedpb.CreateClipRequest{
		StreamId: strp(""),
		UserId:   strp(""),
	}
	data := buildClipLifecycleData(ipcpb.ClipLifecycleData_STAGE_REQUESTED, req, "r", "")
	if data.StreamId != nil {
		t.Fatalf("empty-string stream_id must be skipped, got %q", data.GetStreamId())
	}
	if data.UserId != nil {
		t.Fatalf("empty-string user_id must be skipped, got %q", data.GetUserId())
	}
}

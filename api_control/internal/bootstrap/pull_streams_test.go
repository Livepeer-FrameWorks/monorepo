package bootstrap

import (
	"testing"
)

func TestValidatePullStreamChecksSourceURI(t *testing.T) {
	ps := PullStream{
		PlaybackID:  "frameworks-demo",
		OwnerTenant: TenantRef{Ref: "quartermaster.system_tenant"},
		Title:       "FrameWorks marketing demo",
		SourceURI:   "https://ntv1.akamaized.net/hls/live/2014075/NASA-NTV1-HLS/master.m3u8",
		Enabled:     true,
	}
	if err := validatePullStream(ps); err != nil {
		t.Fatalf("validatePullStream: %v", err)
	}

	ps.SourceURI = "https://example.com/live"
	if err := validatePullStream(ps); err == nil {
		t.Fatal("expected source_uri validation error")
	}
}

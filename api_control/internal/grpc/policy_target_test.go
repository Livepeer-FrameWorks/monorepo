package grpc

import (
	"testing"

	commodorepb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/commodore"
)

func TestPolicyTargetRoutesToStaticCatalog(t *testing.T) {
	tests := []struct {
		name string
		req  *commodorepb.SetPlaybackPolicyRequest
		kind string
	}{
		{name: "stream", req: &commodorepb.SetPlaybackPolicyRequest{StreamId: "stream-id"}, kind: "stream"},
		{name: "vod", req: &commodorepb.SetPlaybackPolicyRequest{VodAssetId: "vod-hash"}, kind: "vod_asset"},
		{name: "clip", req: &commodorepb.SetPlaybackPolicyRequest{ClipId: "clip-hash"}, kind: "clip"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			target, err := pickPolicyTarget(tc.req)
			if err != nil || target.kind != tc.kind {
				t.Fatalf("target = %#v, err = %v; want kind %q", target, err, tc.kind)
			}
		})
	}
}

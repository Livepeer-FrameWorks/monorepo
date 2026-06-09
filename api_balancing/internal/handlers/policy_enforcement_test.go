package handlers

import (
	"context"
	"testing"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	sharedpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/shared"
)

// enforceHTTPResolvePlaybackPolicy is the HTTP analog of the gRPC playback gate
// and must FAIL CLOSED: without a Commodore client it returns false (deny) rather
// than allowing a protected resolve through. The policy evaluation itself is
// unit-tested in triggers.
func TestEnforceHTTPResolvePlaybackPolicy_FailsClosedWithoutCommodore(t *testing.T) {
	prev := commodoreClient
	commodoreClient = nil
	t.Cleanup(func() { commodoreClient = prev })
	if logger == nil {
		logger = logging.NewLogger()
	}

	allowed := enforceHTTPResolvePlaybackPolicy(context.Background(),
		&sharedpb.ViewerEndpointRequest{ContentId: "c1"}, "live+x")
	if allowed {
		t.Fatal("protected HTTP resolve must be denied when policy client is unavailable")
	}
}

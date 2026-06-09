package grpc

import (
	"context"
	"testing"

	"frameworks/api_balancing/internal/control"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	sharedpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/shared"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// enforceResolvePlaybackPolicy must FAIL CLOSED: with no Commodore client wired
// it cannot fetch a policy, so a protected resolve is denied (never silently
// allowed). The policy-evaluation logic itself is unit-tested in triggers; this
// pins the guard that gates it.
func TestEnforceResolvePlaybackPolicy_FailsClosedWithoutCommodore(t *testing.T) {
	prev := control.CommodoreClient
	control.CommodoreClient = nil
	t.Cleanup(func() { control.CommodoreClient = prev })

	s := &FoghornGRPCServer{logger: logging.NewLogger()}
	err := s.enforceResolvePlaybackPolicy(context.Background(),
		&sharedpb.ViewerEndpointRequest{ContentId: "c1"},
		&control.ContentResolution{ContentId: "c1", InternalName: "live+x"})
	if status.Code(err) != codes.PermissionDenied {
		t.Fatalf("want PermissionDenied when policy client unavailable, got %v", err)
	}
}

// handleX402ViewerPayment short-circuits to (false, nil) — "no payment attempted,
// no error" — when the prerequisites are missing (no tenant, no payment header,
// or no purser client). It must not attempt settlement or panic.
func TestHandleX402ViewerPayment_GuardSkips(t *testing.T) {
	s := &FoghornGRPCServer{logger: logging.NewLogger()} // nil purserClient
	cases := []struct{ tenant, header string }{
		{"", "PAY"},  // no tenant
		{"t", ""},    // no header
		{"t", "PAY"}, // has both but nil purser
	}
	for _, c := range cases {
		ok, err := s.handleX402ViewerPayment(context.Background(), c.tenant, "viewer://r", c.header, "1.2.3.4")
		if ok || err != nil {
			t.Fatalf("guard(tenant=%q,header=%q) = (%v,%v), want (false,nil)", c.tenant, c.header, ok, err)
		}
	}
}

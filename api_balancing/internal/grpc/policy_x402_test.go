package grpc

import (
	"context"
	"testing"

	"frameworks/api_balancing/internal/control"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	ipcpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/ipc"
	sharedpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/shared"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type playbackPolicyEvaluatorFunc func(context.Context, string, string, *ipcpb.ViewerConnectTrigger) (string, bool)

func (f playbackPolicyEvaluatorFunc) EvaluateLocalPlaybackPolicy(ctx context.Context, contentID, internalName string, viewer *ipcpb.ViewerConnectTrigger) (string, bool) {
	return f(ctx, contentID, internalName, viewer)
}

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

func TestEnforceResolvePlaybackPolicy_UsesCanonicalLocalEvaluator(t *testing.T) {
	prev := control.CommodoreClient
	control.CommodoreClient = nil
	t.Cleanup(func() { control.CommodoreClient = prev })

	called := false
	s := &FoghornGRPCServer{
		logger: logging.NewLogger(),
		localPlaybackPolicy: playbackPolicyEvaluatorFunc(func(_ context.Context, contentID, internalName string, viewer *ipcpb.ViewerConnectTrigger) (string, bool) {
			called = true
			if contentID != "c1" || internalName != "x" || viewer.GetConnector() != "resolve" {
				t.Fatalf("unexpected local policy input: content=%q internal=%q viewer=%+v", contentID, internalName, viewer)
			}
			return "true", true
		}),
	}
	err := s.enforceResolvePlaybackPolicy(context.Background(),
		&sharedpb.ViewerEndpointRequest{ContentId: "c1"},
		&control.ContentResolution{ContentId: "c1", InternalName: "live+x"})
	if err != nil || !called {
		t.Fatalf("canonical local policy result was not used: called=%v err=%v", called, err)
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

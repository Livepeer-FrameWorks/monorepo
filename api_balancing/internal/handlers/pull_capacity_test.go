package handlers

import (
	"context"
	"net/http"
	"testing"
	"time"

	"frameworks/api_balancing/internal/control"
	"frameworks/api_balancing/internal/state"
	ipcpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/ipc"
	limits "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/tenant_limits"
)

func TestPullSourceCapRefusesUpstreamBeforeOwnerStamp(t *testing.T) {
	withSeededBalancer(t)
	withLoggerSourceRes(t)
	t.Cleanup(control.SetupTestRegistry("", nil))
	previousRegistry := control.StreamRegistryInstance
	control.StreamRegistryInstance = control.NewStreamRegistry(nil, "cluster-local", time.Minute)
	t.Cleanup(func() { control.StreamRegistryInstance = previousRegistry })
	seedSourceNodeCluster("edgeA", "cluster-local")
	lookup := nodePolicyPullLookup("https://origin.example.com/live.m3u8", "edgeA")
	lookup.snapshot.Tenant.Authority.ResourceLimits = &limits.TenantResourceLimits{MaxStreams: 1}
	previous := admitPullUpstreamFn
	t.Cleanup(func() { admitPullUpstreamFn = previous })
	calls := 0
	admitPullUpstreamFn = func(_ context.Context, tenant, stream, internal, node, cluster string, cap int32) (bool, error) {
		calls++
		if tenant != "tenant-a" || stream != "stream-1" || internal != "cam1" || node != "edgeA" || cluster != "cluster-local" || cap != 1 {
			t.Errorf("admission scope: tenant=%q stream=%q internal=%q node=%q cluster=%q cap=%d", tenant, stream, internal, node, cluster, cap)
		}
		return false, nil
	}
	c, w := newSourceCtxSourceRes("source=pull%2Bcam1")
	servePullSourceLookup(c, "pull+cam1", lookup, 0, 0, nil, "edgeA", "203.0.113.5", c.Request.Context(), time.Now())
	if w.Code != http.StatusOK || w.Body.String() != "offline:stream_cap" {
		t.Fatalf("over-cap pull leaked upstream: status=%d body=%q", w.Code, w.Body.String())
	}
	if calls != 1 {
		t.Fatalf("stream-cap admission calls=%d want 1", calls)
	}
	if n := state.DefaultManager().GetNodeState("edgeA"); n.IngressReservedBytesPerSec != 0 {
		t.Fatalf("cap-refused lookup leaked ingress reservation: %d", n.IngressReservedBytesPerSec)
	}
	if owner, known := control.StreamRegistryInstance.SourceOwner("cam1"); known {
		t.Fatalf("denied pull stamped owner %q", owner)
	}
}

func TestReviewBandwidthRefusalDoesNotAcquirePullCapSlot(t *testing.T) {
	withSeededBalancer(t)
	withLoggerSourceRes(t)
	t.Cleanup(control.SetupTestRegistry("", nil))
	seedSourceNodeCluster("edgeA", "cluster-local")
	lookup := nodePolicyPullLookup("https://origin.example.com/live.m3u8", "edgeA")
	state.DefaultManager().SetNodeCapacity("edgeA", &ipcpb.NodeBandwidthCapacity{EgressBudgetBytesPerSec: 1, IngressBudgetBytesPerSec: 1})
	previous := admitPullUpstreamFn
	t.Cleanup(func() { admitPullUpstreamFn = previous })
	calls := 0
	admitPullUpstreamFn = func(context.Context, string, string, string, string, string, int32) (bool, error) {
		calls++
		return true, nil
	}
	c, w := newSourceCtxSourceRes("source=pull%2Bcam1")
	servePullSourceLookup(c, "pull+cam1", lookup, 0, 0, nil, "edgeA", "203.0.113.5", c.Request.Context(), time.Now())
	if calls != 0 {
		t.Errorf("bandwidth-refused pull acquired tenant cap slot: admission calls=%d", calls)
	}
	if w.Body.String() != "offline:bandwidth_capacity" {
		t.Errorf("bandwidth refusal lost its diagnostic: %q", w.Body.String())
	}
}

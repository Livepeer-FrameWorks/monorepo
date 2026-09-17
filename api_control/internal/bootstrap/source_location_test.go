package bootstrap

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"

	"frameworks/api_control/internal/placementpolicy"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/placement"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type recordingNodeChecker struct {
	err    error
	calls  int
	tenant string
	nodes  []placementpolicy.PlacementNode
}

func (c *recordingNodeChecker) CheckTenantPlacementNodes(_ context.Context, tenantID string, nodes []placementpolicy.PlacementNode) error {
	c.calls++
	c.tenant, c.nodes = tenantID, slices.Clone(nodes)
	return c.err
}

func nodeBearingSection() CommodoreSection {
	pull := validPullStream()
	pull.OwnerTenant = TenantRef{Ref: "quartermaster.system_tenant"}
	pull.SourceLocation = &SourceLocation{Clusters: []SourceLocationCluster{{ClusterID: "edge-b", NodeIDs: []string{"edge-b-1"}}}}
	mist := validMistNativeStream()
	mist.SourceLocation = &SourceLocation{Clusters: []SourceLocationCluster{{ClusterID: "edge-a"}}, AvoidNodeIDs: []string{"edge-a-9"}}
	return CommodoreSection{PullStreams: []PullStream{pull}, MistNativeStreams: []MistNativeStream{mist}}
}

func TestRequireSourceLocationNodePlacementRefusesUntilCellsAttest(t *testing.T) {
	checker := &recordingNodeChecker{err: placement.NodePlacementNotReadyError("cells not ready")}
	err := RequireSourceLocationNodePlacement(context.Background(), nodeBearingSection(), staticResolver("system-tenant-id"), checker)
	if err == nil {
		t.Fatal("node-bearing source locations passed without node placement readiness")
	}
	for _, want := range []string{`pull_stream "pb1"`, `mist_native_stream "mp1"`, "every media cell", "upgrade Foghorn in every cell, then rerun bootstrap"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("refusal %q does not mention %q", err, want)
		}
	}
	// The allowed node carries the cluster it is listed under; the avoided node
	// needs ownership only.
	if checker.calls != 1 || checker.tenant != "system-tenant-id" || fmt.Sprint(checker.nodes) != "[{edge-a-9 []} {edge-b-1 [edge-b]}]" {
		t.Fatalf("gate saw calls=%d tenant=%q nodes=%v", checker.calls, checker.tenant, checker.nodes)
	}
}

func TestRequireSourceLocationNodePlacementPassesWhenGateAdmits(t *testing.T) {
	checker := &recordingNodeChecker{}
	if err := RequireSourceLocationNodePlacement(context.Background(), nodeBearingSection(), staticResolver("system-tenant-id"), checker); err != nil {
		t.Fatalf("admitted node placement refused: %v", err)
	}
	if checker.calls != 1 {
		t.Fatalf("gate calls = %d, want 1", checker.calls)
	}
}

func TestRequireSourceLocationNodePlacementSkipsLocationsWithoutNodes(t *testing.T) {
	section := CommodoreSection{PullStreams: []PullStream{validPullStream()}, MistNativeStreams: []MistNativeStream{validMistNativeStream()}}
	if err := RequireSourceLocationNodePlacement(context.Background(), section, nil, nil); err != nil {
		t.Fatalf("cluster-only locations needed the node gate: %v", err)
	}
	requirements, err := NodeSourceLocationRequirements(section)
	if err != nil || len(requirements) != 0 {
		t.Fatalf("cluster-only requirements = %v, %v", requirements, err)
	}
}

func TestRequireSourceLocationNodePlacementKeepsOtherRefusals(t *testing.T) {
	ownership := status.Error(codes.InvalidArgument, `node "edge-b-1" is not in a media cluster this tenant owns`)
	err := RequireSourceLocationNodePlacement(context.Background(), nodeBearingSection(), staticResolver("tenant"), &recordingNodeChecker{err: ownership})
	if !errors.Is(err, ownership) || strings.Contains(err.Error(), "upgrade Foghorn") {
		t.Fatalf("ownership refusal reported as %v", err)
	}
}

func TestNodeSourceLocationRequirementsNamesEveryNodeBearingStream(t *testing.T) {
	requirements, err := NodeSourceLocationRequirements(nodeBearingSection())
	if err != nil || len(requirements) != 2 {
		t.Fatalf("requirements = %v, %v", requirements, err)
	}
	if !strings.Contains(requirements[0], `pull_stream "pb1"`) || !strings.Contains(requirements[1], `mist_native_stream "mp1"`) {
		t.Fatalf("requirements do not name the streams: %v", requirements)
	}
}

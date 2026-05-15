package resolvers

import (
	"context"
	"testing"

	"frameworks/api_gateway/graph/model"
	pb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto"
)

func TestNodeModeFieldsSkipNonEdgeNodes(t *testing.T) {
	resolver := &Resolver{}
	node := &pb.InfrastructureNode{
		NodeId:   "central-eu-1",
		NodeType: "core",
	}

	mode, err := resolver.DoNodeEffectiveMode(context.Background(), node)
	if err != nil {
		t.Fatalf("DoNodeEffectiveMode returned error: %v", err)
	}
	if mode != model.NodeOperationalModeNormal {
		t.Fatalf("DoNodeEffectiveMode = %s, want %s", mode, model.NodeOperationalModeNormal)
	}

	impact, err := resolver.DoNodeRoutingImpactPreview(context.Background(), node)
	if err != nil {
		t.Fatalf("DoNodeRoutingImpactPreview returned error: %v", err)
	}
	if impact == nil || impact.ActiveStreams != 0 || impact.ActiveViewers != 0 {
		t.Fatalf("DoNodeRoutingImpactPreview = %#v, want zero-value impact", impact)
	}
}

func TestNodeModeFieldsStillRequireControlPlaneForEdgeNodes(t *testing.T) {
	resolver := &Resolver{}
	node := &pb.InfrastructureNode{
		NodeId:   "regional-eu-1",
		NodeType: "edge",
	}

	if _, err := resolver.DoNodeEffectiveMode(context.Background(), node); err == nil {
		t.Fatal("DoNodeEffectiveMode returned nil error without Commodore client")
	}
	if _, err := resolver.DoNodeRoutingImpactPreview(context.Background(), node); err == nil {
		t.Fatal("DoNodeRoutingImpactPreview returned nil error without Commodore client")
	}
}

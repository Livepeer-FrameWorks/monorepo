package resolvers

import (
	"context"
	"testing"

	"frameworks/api_gateway/graph/model"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/globalid"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
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

func TestNodeModeFieldsDefaultWhenControlPlaneUnavailable(t *testing.T) {
	resolver := &Resolver{Logger: logging.NewLogger()}
	node := &pb.InfrastructureNode{
		NodeId:   "regional-eu-1",
		NodeType: "edge",
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

func TestNormalizeNodeModeIDAcceptsRawAndRelayIDs(t *testing.T) {
	raw, validationErr := normalizeNodeModeID(" regional-eu-1 ")
	if validationErr != nil {
		t.Fatalf("normalizeNodeModeID returned validation error: %v", validationErr)
	}
	if raw != "regional-eu-1" {
		t.Fatalf("normalizeNodeModeID raw = %q, want regional-eu-1", raw)
	}

	raw, validationErr = normalizeNodeModeID(globalid.Encode(globalid.TypeInfrastructureNode, "regional-eu-1"))
	if validationErr != nil {
		t.Fatalf("normalizeNodeModeID returned validation error for relay ID: %v", validationErr)
	}
	if raw != "regional-eu-1" {
		t.Fatalf("normalizeNodeModeID relay = %q, want regional-eu-1", raw)
	}
}

func TestNormalizeNodeModeIDRejectsWrongRelayType(t *testing.T) {
	_, validationErr := normalizeNodeModeID(globalid.Encode(globalid.TypeCluster, "regional-eu"))
	if validationErr == nil {
		t.Fatal("expected validation error")
	}
	if validationErr.Field == nil || *validationErr.Field != "nodeId" {
		t.Fatalf("validation field = %#v, want nodeId", validationErr.Field)
	}
}

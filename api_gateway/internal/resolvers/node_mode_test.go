package resolvers

import (
	"context"
	"testing"

	"frameworks/api_gateway/graph/model"
	pb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
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

func TestNodeHealthSoftFailure(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{name: "unavailable", err: status.Error(codes.Unavailable, "foghorn unavailable"), want: true},
		{name: "deadline", err: context.DeadlineExceeded, want: true},
		{name: "permission denied", err: status.Error(codes.PermissionDenied, "denied"), want: false},
		{name: "invalid argument", err: status.Error(codes.InvalidArgument, "bad node"), want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := nodeHealthSoftFailure(tt.err); got != tt.want {
				t.Fatalf("nodeHealthSoftFailure() = %v, want %v", got, tt.want)
			}
		})
	}
}

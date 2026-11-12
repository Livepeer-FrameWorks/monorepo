package main

import (
	"context"
	"testing"

	quartermasterpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/quartermaster"
)

type aliasGeographyReader struct {
	nodes []*quartermasterpb.InfrastructureNode
}

func (r aliasGeographyReader) GetNode(context.Context, string) (*quartermasterpb.NodeResponse, error) {
	return nil, nil
}
func (r aliasGeographyReader) GetCluster(context.Context, string) (*quartermasterpb.ClusterResponse, error) {
	return nil, nil
}
func (r aliasGeographyReader) ListHealthyNodesForDNSForCluster(context.Context, int, string, string) (*quartermasterpb.ListHealthyNodesForDNSResponse, error) {
	return &quartermasterpb.ListHealthyNodesForDNSResponse{Nodes: r.nodes}, nil
}
func TestTenantAliasAddressAdapterPreservesGeography(t *testing.T) {
	lat, lon := 39.0, -77.0
	ip := "192.0.2.10"
	reader := aliasGeographyReader{nodes: []*quartermasterpb.InfrastructureNode{{NodeId: "us", ExternalIp: &ip, Latitude: &lat, Longitude: &lon}}}
	got, err := (quartermasterEdgeResolver{qm: reader}).ResolveServiceAddressesForClusters(t.Context(), "foghorn", []string{"media-us-1"}, 90)
	if err != nil || len(got) != 1 {
		t.Fatalf("addresses: %v, %v", got, err)
	}
	if got[0].Latitude == nil || got[0].Longitude == nil || *got[0].Latitude != lat || *got[0].Longitude != lon {
		t.Fatalf("alias adapter lost node coordinates: %+v", got[0])
	}
}

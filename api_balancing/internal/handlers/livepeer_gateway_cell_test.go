package handlers

import (
	"testing"

	"frameworks/api_balancing/internal/control"
)

// The stack reproduction: a tenant on virtual cluster demo-media, served by the
// central-primary Foghorn, gets a token naming demo-media (the cluster its
// gateway is assigned to); the gateway's auth webhook lands on central-primary.
func TestLivepeerGatewayCellAllowedForServedVirtualCluster(t *testing.T) {
	served := map[string]bool{"central-primary": true, "demo-media": true}
	orig := servesLivepeerGatewayCluster
	servesLivepeerGatewayCluster = func(id string) bool { return served[id] }
	t.Cleanup(func() { servesLivepeerGatewayCluster = orig })

	claims := control.TranscodeJobClaims{AllowedGatewayClusterIDs: []string{"demo-media"}}
	if !livepeerGatewayCellAllowed(claims, "central-primary", "central-primary") {
		t.Fatal("a token naming a cluster this Foghorn serves must be accepted")
	}
	foreign := control.TranscodeJobClaims{AllowedGatewayClusterIDs: []string{"us-primary"}}
	if livepeerGatewayCellAllowed(foreign, "central-primary", "central-primary") {
		t.Fatal("a token naming another cell's cluster must be refused")
	}
	own := control.TranscodeJobClaims{AllowedGatewayClusterIDs: []string{"central-primary"}}
	if !livepeerGatewayCellAllowed(own, "central-primary", "central-primary") {
		t.Fatal("a token naming this Foghorn's own cluster must be accepted")
	}
}

func TestLivepeerGatewayAudienceUsesOpaqueControlCell(t *testing.T) {
	previous := servesLivepeerGatewayCluster
	servesLivepeerGatewayCluster = func(id string) bool { return id == "assigned-private-cluster" }
	t.Cleanup(func() { servesLivepeerGatewayCluster = previous })
	own := control.TranscodeJobClaims{AllowedGatewayClusterIDs: []string{"opaque-us-cell"}}
	if !livepeerGatewayCellAllowed(own, "physical-media-us", "opaque-us-cell") {
		t.Fatal("gateway rejected the exact owning control cell because its registry cluster differs")
	}
	foreign := control.TranscodeJobClaims{AllowedGatewayClusterIDs: []string{"opaque-eu-cell"}}
	if livepeerGatewayCellAllowed(foreign, "physical-media-us", "opaque-us-cell") {
		t.Fatal("gateway accepted another control cell's audience")
	}
}

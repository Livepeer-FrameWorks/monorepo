package cmd

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"testing"

	"frameworks/cli/pkg/inventory"
	commonpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/common"
	quartermasterpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/quartermaster"
	"github.com/spf13/cobra"
)

func TestLivepeerReadResolutionPrecedence(t *testing.T) {
	cmd := &cobra.Command{Use: "test"}
	cmd.Flags().String("address", "0x1111111111111111111111111111111111111111", "")
	cmd.Flags().String("rpc", "https://explicit.example", "")
	manifest := &livepeerManifest{cmd: cmd}
	defer manifest.cleanup()
	wallets, err := resolveLivepeerWallets(cmd, manifest)
	if err != nil || len(wallets) != 1 || wallets[0].Address != "0x1111111111111111111111111111111111111111" {
		t.Fatalf("explicit wallets=%+v err=%v", wallets, err)
	}
	rpc, err := resolveLivepeerRPC(cmd, manifest)
	if err != nil || rpc != "https://explicit.example" {
		t.Fatalf("explicit rpc=%q err=%v", rpc, err)
	}
	if manifest.rc != nil {
		t.Fatal("explicit --address and --rpc must not resolve a manifest")
	}

	envRPC, err := livepeerRPCFromEnv(map[string]string{"ARBITRUM_RPC_ENDPOINTS": "https://first.example,https://second.example"})
	if err != nil || envRPC != "https://first.example" {
		t.Fatalf("env rpc=%q err=%v", envRPC, err)
	}
}

func livepeerStagingManifest() *inventory.Manifest {
	return &inventory.Manifest{
		Clusters: map[string]inventory.ClusterConfig{
			"staging-core": {}, "staging-media-eu": {}, "staging-media-us": {},
		},
		Services: map[string]inventory.ServiceConfig{
			"quartermaster":       {Enabled: true, Cluster: "staging-core"},
			"livepeer-gateway-eu": {Enabled: true, Deploy: "livepeer-gateway", Cluster: "staging-media-eu", Hosts: []string{"fw-stg-eu-1"}},
			"livepeer-gateway-us": {Enabled: true, Deploy: "livepeer-gateway", Cluster: "staging-media-us", Hosts: []string{"fw-stg-us-1"}},
			"livepeer-gateway-x":  {Enabled: false, Deploy: "livepeer-gateway", Cluster: "staging-media-x"},
		},
	}
}

// The gitops cluster name ("staging") selects the manifest; Quartermaster
// discovery needs the media clusters the gateways are assigned to.
func TestLivepeerDiscoveryClusterIDsComeFromGatewayServices(t *testing.T) {
	manifest := livepeerStagingManifest()
	for _, flag := range []string{"", "staging"} {
		ids, err := livepeerDiscoveryClusterIDs(manifest, flag)
		if err != nil || strings.Join(ids, ",") != "staging-media-eu,staging-media-us" {
			t.Fatalf("--cluster=%q: ids=%v err=%v, want every gateway cluster", flag, ids, err)
		}
	}
	ids, err := livepeerDiscoveryClusterIDs(manifest, "staging-media-us")
	if err != nil || strings.Join(ids, ",") != "staging-media-us" {
		t.Fatalf("--cluster=staging-media-us: ids=%v err=%v", ids, err)
	}
	if _, err := livepeerDiscoveryClusterIDs(&inventory.Manifest{}, ""); err == nil {
		t.Fatal("manifest without a livepeer-gateway service must not discover anything")
	}
}

type fakeLivepeerDiscovery struct {
	byCluster map[string]*quartermasterpb.ServiceDiscoveryResponse
	queried   []string
}

func (f *fakeLivepeerDiscovery) DiscoverServices(_ context.Context, serviceType, clusterID string, _ *commonpb.CursorPaginationRequest) (*quartermasterpb.ServiceDiscoveryResponse, error) {
	f.queried = append(f.queried, clusterID)
	if serviceType != "livepeer-gateway" || clusterID == "" {
		return nil, fmt.Errorf("unexpected discovery %q/%q", serviceType, clusterID)
	}
	if resp := f.byCluster[clusterID]; resp != nil {
		return resp, nil
	}
	return &quartermasterpb.ServiceDiscoveryResponse{}, nil
}

func TestDiscoverLivepeerWalletsAcrossGatewayClusters(t *testing.T) {
	shared := "0x8C9f0152000000000000000000000000000e3E6f"
	other := "0x2222222222222222222222222222222222222222"
	client := &fakeLivepeerDiscovery{byCluster: map[string]*quartermasterpb.ServiceDiscoveryResponse{
		"staging-media-eu": {Instances: []*quartermasterpb.ServiceInstance{
			{Status: "stopped", Metadata: map[string]string{"wallet_address": other}},
			{Status: "running", Metadata: map[string]string{"wallet_address": shared}},
		}},
		"staging-media-us": {Instances: []*quartermasterpb.ServiceInstance{
			{Status: "running", Metadata: map[string]string{"wallet_address": strings.ToLower(shared)}},
		}},
	}}
	wallets, err := discoverLivepeerWallets(context.Background(), client, []string{"staging-media-eu", "staging-media-us"})
	if err != nil {
		t.Fatal(err)
	}
	if len(wallets) != 1 || wallets[0].Address != shared || strings.Join(wallets[0].ClusterIDs, ",") != "staging-media-eu,staging-media-us" {
		t.Fatalf("wallets=%+v, want the shared wallet once with both clusters", wallets)
	}
	if strings.Join(client.queried, ",") != "staging-media-eu,staging-media-us" {
		t.Fatalf("queried %v", client.queried)
	}

	if _, err := discoverLivepeerWallets(context.Background(), &fakeLivepeerDiscovery{}, []string{"staging-media-eu"}); err == nil {
		t.Fatal("no running gateway with wallet metadata must be an error")
	}
}

func TestLivepeerMutationTargetClusterFilter(t *testing.T) {
	manifest := livepeerStagingManifest()
	for flag, want := range map[string]bool{"": true, "staging": true, "staging-media-eu": true, "staging-media-us": false} {
		if got := livepeerGatewayMatchesClusterFlag(manifest, "livepeer-gateway-eu", manifest.Services["livepeer-gateway-eu"], flag); got != want {
			t.Fatalf("--cluster=%q matches livepeer-gateway-eu = %v, want %v", flag, got, want)
		}
	}
}

func TestLivepeerReserveMutationUsesReserveAmount(t *testing.T) {
	args := livepeerMutationCurlArgs(7935, "/fundDepositAndReserve", []string{"depositAmount=0", "reserveAmount=123"})
	if !slices.Contains(args, "reserveAmount=123") || !slices.Contains(args, "depositAmount=0") {
		t.Fatalf("missing reserve form fields: %#v", args)
	}
	for _, arg := range args {
		if strings.Contains(arg, "penaltyEscrowAmount") {
			t.Fatalf("legacy wrong form key survived: %#v", args)
		}
	}
	if args[len(args)-1] != "http://127.0.0.1:7935/fundDepositAndReserve" {
		t.Fatalf("mutation is not loopback-bound: %#v", args)
	}
}

func TestLivepeerMutation404ReturnsRunbook(t *testing.T) {
	err := livepeerMutationHTTPError(404, "404 page not found", "livepeer-gateway-eu")
	if err == nil || !strings.Contains(err.Error(), `enable_cli_tx_routes: "true"`) || !strings.Contains(err.Error(), "then remove it") {
		t.Fatalf("missing tx-route runbook: %v", err)
	}
	body, status, err := splitCurlStatus("unlock success\n200\n")
	if err != nil || body != "unlock success" || status != 200 {
		t.Fatalf("body=%q status=%d err=%v", body, status, err)
	}
}

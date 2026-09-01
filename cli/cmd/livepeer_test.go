package cmd

import (
	"slices"
	"strings"
	"testing"

	quartermasterpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/quartermaster"
	"github.com/spf13/cobra"
)

func TestLivepeerReadResolutionPrecedence(t *testing.T) {
	cmd := &cobra.Command{Use: "test"}
	cmd.Flags().String("address", "0x1111111111111111111111111111111111111111", "")
	cmd.Flags().String("rpc", "https://explicit.example", "")
	address, err := discoverLivepeerWalletAddress(cmd)
	if err != nil || address != "0x1111111111111111111111111111111111111111" {
		t.Fatalf("explicit address=%q err=%v", address, err)
	}
	rpc, err := resolveLivepeerRPC(cmd)
	if err != nil || rpc != "https://explicit.example" {
		t.Fatalf("explicit rpc=%q err=%v", rpc, err)
	}

	discovered, err := livepeerWalletFromDiscovery(&quartermasterpb.ServiceDiscoveryResponse{Instances: []*quartermasterpb.ServiceInstance{
		{Status: "stopped", Metadata: map[string]string{"wallet_address": "0xdead"}},
		{Status: "running", Metadata: map[string]string{"wallet_address": "0x2222222222222222222222222222222222222222"}},
	}})
	if err != nil || discovered != "0x2222222222222222222222222222222222222222" {
		t.Fatalf("discovered address=%q err=%v", discovered, err)
	}
	envRPC, err := livepeerRPCFromEnv(map[string]string{"ARBITRUM_RPC_ENDPOINTS": "https://first.example,https://second.example"})
	if err != nil || envRPC != "https://first.example" {
		t.Fatalf("env rpc=%q err=%v", envRPC, err)
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

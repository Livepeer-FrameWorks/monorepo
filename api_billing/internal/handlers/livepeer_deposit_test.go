package handlers

import (
	"context"
	"math/big"
	"testing"

	"frameworks/pkg/logging"
	pb "frameworks/pkg/proto"
)

func TestEthToWei_OneETH(t *testing.T) {
	wei := ethToWei(1.0)
	expected := new(big.Int).Exp(big.NewInt(10), big.NewInt(18), nil) // 1e18
	if wei.Cmp(expected) != 0 {
		t.Fatalf("expected %s, got %s", expected, wei)
	}
}

func TestEthToWei_FractionalETH(t *testing.T) {
	wei := ethToWei(0.2)
	expected := new(big.Int).Mul(big.NewInt(2), new(big.Int).Exp(big.NewInt(10), big.NewInt(17), nil)) // 2e17
	if wei.Cmp(expected) != 0 {
		t.Fatalf("expected %s, got %s", expected, wei)
	}
}

func TestEthToWei_Zero(t *testing.T) {
	wei := ethToWei(0)
	if wei.Cmp(big.NewInt(0)) != 0 {
		t.Fatalf("expected 0, got %s", wei)
	}
}

func TestWeiToETH_Roundtrip(t *testing.T) {
	original := 0.123456
	wei := ethToWei(original)
	result := weiToETH(wei)
	diff := original - result
	if diff < 0 {
		diff = -diff
	}
	if diff > 1e-10 {
		t.Fatalf("roundtrip: expected %f, got %f (diff: %e)", original, result, diff)
	}
}

type fakeLivepeerServiceDiscoveryClient struct {
	resp *pb.ServiceDiscoveryResponse
	err  error
}

func (f *fakeLivepeerServiceDiscoveryClient) DiscoverServices(_ context.Context, _, _ string, _ *pb.CursorPaginationRequest) (*pb.ServiceDiscoveryResponse, error) {
	return f.resp, f.err
}

func TestDiscoverGatewayAddressesUsesMetadataAndDeduplicatesWallets(t *testing.T) {
	monitor := &LivepeerDepositMonitor{
		logger: logging.NewLogger(),
		qm: &fakeLivepeerServiceDiscoveryClient{
			resp: &pb.ServiceDiscoveryResponse{
				Instances: []*pb.ServiceInstance{
					{
						Status:   "running",
						Host:     stringPtr("10.0.0.1"),
						Port:     int32Ptr(8935),
						Metadata: map[string]string{"wallet_address": "0xABC123"},
					},
					{
						Status:   "running",
						Host:     stringPtr("10.0.0.2"),
						Port:     int32Ptr(8935),
						Metadata: map[string]string{"wallet_address": "0xabc123"},
					},
					{
						Status:   "running",
						Host:     stringPtr("10.0.0.3"),
						Port:     int32Ptr(8935),
						Metadata: map[string]string{"wallet_address": "0xdef456"},
					},
				},
			},
		},
	}

	gateways := monitor.discoverGatewayAddresses(context.Background())
	if len(gateways) != 2 {
		t.Fatalf("expected 2 unique gateway wallets, got %d", len(gateways))
	}
	if gateways[0].address != "0xabc123" {
		t.Fatalf("expected normalized wallet address, got %q", gateways[0].address)
	}
	if gateways[1].address != "0xdef456" {
		t.Fatalf("expected second wallet address, got %q", gateways[1].address)
	}
}

func TestDiscoverGatewayAddressesSkipsMissingWalletMetadata(t *testing.T) {
	monitor := &LivepeerDepositMonitor{
		logger: logging.NewLogger(),
		qm: &fakeLivepeerServiceDiscoveryClient{
			resp: &pb.ServiceDiscoveryResponse{
				Instances: []*pb.ServiceInstance{
					{
						Status: "running",
						Host:   stringPtr("10.0.0.1"),
						Port:   int32Ptr(8935),
					},
				},
			},
		},
	}

	gateways := monitor.discoverGatewayAddresses(context.Background())
	if len(gateways) != 0 {
		t.Fatalf("expected no gateways without wallet metadata, got %d", len(gateways))
	}
}

func stringPtr(v string) *string {
	return &v
}

func int32Ptr(v int32) *int32 {
	return &v
}

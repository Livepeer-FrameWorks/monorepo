package handlers

import (
	"testing"
)

func TestNetworkByChainID_Ethereum(t *testing.T) {
	n, ok := NetworkByChainID(1)
	if !ok {
		t.Fatal("expected ok for chain ID 1")
	}
	if n.Name != "ethereum" {
		t.Fatalf("expected ethereum, got %q", n.Name)
	}
	if n.ChainID != 1 {
		t.Fatalf("expected chain ID 1, got %d", n.ChainID)
	}
}

func TestNetworkByChainID_Base(t *testing.T) {
	n, ok := NetworkByChainID(8453)
	if !ok {
		t.Fatal("expected ok for chain ID 8453")
	}
	if n.Name != "base" {
		t.Fatalf("expected base, got %q", n.Name)
	}
}

func TestNetworkByChainID_Arbitrum(t *testing.T) {
	n, ok := NetworkByChainID(42161)
	if !ok {
		t.Fatal("expected ok for chain ID 42161")
	}
	if n.Name != "arbitrum" {
		t.Fatalf("expected arbitrum, got %q", n.Name)
	}
}

func TestNetworkByChainID_Unknown(t *testing.T) {
	_, ok := NetworkByChainID(999)
	if ok {
		t.Fatal("expected not ok for unknown chain ID")
	}
}

func TestX402Networks_MainnetsOnly(t *testing.T) {
	nets := X402Networks(false)
	if len(nets) != 2 {
		t.Fatalf("expected 2 mainnet x402 networks, got %d", len(nets))
	}
	for _, n := range nets {
		if !n.X402Enabled {
			t.Fatalf("network %q should be x402 enabled", n.Name)
		}
		if n.IsTestnet {
			t.Fatalf("network %q should not be testnet", n.Name)
		}
	}
}

func TestX402Networks_IncludeTestnets(t *testing.T) {
	nets := X402Networks(true)
	if len(nets) != 4 {
		t.Fatalf("expected 4 x402 networks (2 mainnet + 2 testnet), got %d", len(nets))
	}
}

func TestX402Networks_EthereumExcluded(t *testing.T) {
	nets := X402Networks(true)
	for _, n := range nets {
		if n.Name == "ethereum" {
			t.Fatal("ethereum should not be in x402 networks (X402Enabled=false)")
		}
	}
}

func TestDepositNetworks_MainnetsOnly(t *testing.T) {
	nets := DepositNetworks(false)
	if len(nets) != 3 {
		t.Fatalf("expected 3 mainnet networks, got %d", len(nets))
	}
	for _, n := range nets {
		if n.IsTestnet {
			t.Fatalf("network %q should not be testnet", n.Name)
		}
	}
}

func TestDepositNetworks_IncludeTestnets(t *testing.T) {
	nets := DepositNetworks(true)
	if len(nets) != 5 {
		t.Fatalf("expected 5 total networks, got %d", len(nets))
	}
}

func TestDefaultX402Network(t *testing.T) {
	n := DefaultX402Network()
	if n.ChainID != 8453 {
		t.Fatalf("expected chain ID 8453 (Base), got %d", n.ChainID)
	}
	if n.Name != "base" {
		t.Fatalf("expected base, got %q", n.Name)
	}
	if !n.X402Enabled {
		t.Fatal("default x402 network should have X402Enabled=true")
	}
}

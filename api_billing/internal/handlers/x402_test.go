package handlers

import (
	"testing"

	"github.com/sirupsen/logrus"
)

func TestDeriveAddressFromPrivKey(t *testing.T) {
	address, err := deriveAddressFromPrivKey("4f3edf983ac636a65a842ce7c78d9aa706d3b113b37f1f6f0f6a16c3b7f1f941")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if address != "0x90f8bf6a479f320ead074411a4b0e7944ea8c9c1" {
		t.Fatalf("unexpected address: %s", address)
	}
}

func TestGetNetworkConfigRespectsX402Settings(t *testing.T) {
	handler := &X402Handler{logger: logrus.New(), includeTestnets: false}

	if _, err := handler.getNetworkConfig("ethereum"); err == nil {
		t.Fatal("expected error for x402-disabled network")
	}

	if _, err := handler.getNetworkConfig("base-sepolia"); err == nil {
		t.Fatal("expected error for testnet when disabled")
	}

	if _, err := handler.getNetworkConfig("base"); err != nil {
		t.Fatalf("expected base network to be allowed, got %v", err)
	}
}

func TestGetNetworkConfigAllowsTestnetsWhenEnabled(t *testing.T) {
	handler := &X402Handler{logger: logrus.New(), includeTestnets: true}

	if _, err := handler.getNetworkConfig("base-sepolia"); err != nil {
		t.Fatalf("expected testnet to be allowed, got %v", err)
	}
}

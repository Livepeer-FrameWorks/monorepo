package config

import "testing"

func TestGatewayMCPEndpointUsesExplicitInternalURL(t *testing.T) {
	cfg := Config{
		GatewayPublicURL: "https://bridge.frameworks.network",
		GatewayMCPURL:    "http://bridge.internal:18090/mcp/",
	}

	if got := cfg.GatewayMCPEndpoint(); got != "http://bridge.internal:18090/mcp" {
		t.Fatalf("GatewayMCPEndpoint got %q", got)
	}
}

func TestGatewayMCPEndpointFallsBackToPublicURL(t *testing.T) {
	cfg := Config{GatewayPublicURL: "https://bridge.frameworks.network/"}

	if got := cfg.GatewayMCPEndpoint(); got != "https://bridge.frameworks.network/mcp" {
		t.Fatalf("GatewayMCPEndpoint got %q", got)
	}
}

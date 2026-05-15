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

func TestGatewayMCPEndpointsPreferExplicitInternalURLList(t *testing.T) {
	cfg := Config{
		GatewayPublicURL: "https://bridge.frameworks.network",
		GatewayMCPURL:    "http://bridge.internal:18090/mcp",
		GatewayMCPURLs: []string{
			"http://bridge-eu-1.internal:18000/mcp/",
			"http://bridge-us-1.internal:18000/mcp",
			"http://bridge-eu-1.internal:18000/mcp",
		},
	}

	got := cfg.GatewayMCPEndpoints()
	want := []string{"http://bridge-eu-1.internal:18000/mcp", "http://bridge-us-1.internal:18000/mcp"}
	if len(got) != len(want) {
		t.Fatalf("GatewayMCPEndpoints got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("GatewayMCPEndpoints got %v, want %v", got, want)
		}
	}
}

func TestGatewayMCPEndpointFallsBackToPublicURL(t *testing.T) {
	cfg := Config{GatewayPublicURL: "https://bridge.frameworks.network/"}

	if got := cfg.GatewayMCPEndpoint(); got != "https://bridge.frameworks.network/mcp" {
		t.Fatalf("GatewayMCPEndpoint got %q", got)
	}
}

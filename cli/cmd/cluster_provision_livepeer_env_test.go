package cmd

import (
	"os"
	"path/filepath"
	"testing"

	"frameworks/cli/pkg/inventory"
	"frameworks/cli/pkg/orchestrator"
)

func TestBuildServiceEnvVarsMapsLivepeerRPCFromNetworkEnv(t *testing.T) {
	envFile := writeTestEnvFile(t, "ARBITRUM_RPC_ENDPOINT=https://arb.example\n")

	manifest := &inventory.Manifest{
		EnvFiles: []string{envFile},
		Services: map[string]inventory.ServiceConfig{
			"livepeer-gateway": {
				Enabled: true,
				Config: map[string]string{
					"network": "arbitrum-one-mainnet",
				},
			},
		},
	}

	env, err := buildServiceEnvVars(&orchestrator.Task{
		Name: "livepeer-gateway",
		Type: "livepeer-gateway",
	}, manifest, map[string]interface{}{}, "", "")
	if err != nil {
		t.Fatalf("buildServiceEnvVars returned error: %v", err)
	}

	if got := env["eth_url"]; got != "https://arb.example" {
		t.Fatalf("expected eth_url from ARBITRUM_RPC_ENDPOINT, got %q", got)
	}
}

func TestBuildServiceEnvVarsPrefersExplicitLivepeerConfig(t *testing.T) {
	envFile := writeTestEnvFile(t, "ARBITRUM_RPC_ENDPOINT=https://arb.example\n")

	manifest := &inventory.Manifest{
		EnvFiles: []string{envFile},
		Services: map[string]inventory.ServiceConfig{
			"livepeer-gateway": {
				Enabled: true,
				Config: map[string]string{
					"network": "arbitrum-one-mainnet",
					"eth_url": "https://override.example",
				},
			},
		},
	}

	env, err := buildServiceEnvVars(&orchestrator.Task{
		Name: "livepeer-gateway",
		Type: "livepeer-gateway",
	}, manifest, map[string]interface{}{}, "", "")
	if err != nil {
		t.Fatalf("buildServiceEnvVars returned error: %v", err)
	}

	if got := env["eth_url"]; got != "https://override.example" {
		t.Fatalf("expected explicit eth_url override, got %q", got)
	}
}

func TestBuildServiceEnvVarsMapsLivepeerUppercaseAliases(t *testing.T) {
	envFile := writeTestEnvFile(t, ""+
		"ARBITRUM_RPC_ENDPOINT=https://arb.example\n"+
		"LIVEPEER_ETH_ACCT_ADDR=0xabc123\n"+
		"LIVEPEER_ORCH_WEBHOOK_URL=https://orch.example\n"+
		"LIVEPEER_REMOTE_SIGNER_URL=https://signer.example\n"+
		"LIVEPEER_AUTH_WEBHOOK_URL=https://auth.example\n"+
		"LIVEPEER_GATEWAY_HOST=livepeer.example\n")

	manifest := &inventory.Manifest{
		EnvFiles: []string{envFile},
		Services: map[string]inventory.ServiceConfig{
			"livepeer-gateway": {
				Enabled: true,
				Config: map[string]string{
					"network": "arbitrum-one-mainnet",
				},
			},
		},
	}

	env, err := buildServiceEnvVars(&orchestrator.Task{
		Name: "livepeer-gateway",
		Type: "livepeer-gateway",
	}, manifest, map[string]interface{}{}, "", "")
	if err != nil {
		t.Fatalf("buildServiceEnvVars returned error: %v", err)
	}

	if got := env["eth_acct_addr"]; got != "0xabc123" {
		t.Fatalf("expected eth_acct_addr from LIVEPEER_ETH_ACCT_ADDR, got %q", got)
	}
	if got := env["orch_webhook_url"]; got != "https://orch.example" {
		t.Fatalf("expected orch_webhook_url from LIVEPEER_ORCH_WEBHOOK_URL, got %q", got)
	}
	if got := env["remote_signer_url"]; got != "https://signer.example" {
		t.Fatalf("expected remote_signer_url from LIVEPEER_REMOTE_SIGNER_URL, got %q", got)
	}
	if got := env["auth_webhook_url"]; got != "https://auth.example" {
		t.Fatalf("expected auth_webhook_url from LIVEPEER_AUTH_WEBHOOK_URL, got %q", got)
	}
	if got := env["gateway_host"]; got != "livepeer.example" {
		t.Fatalf("expected gateway_host from LIVEPEER_GATEWAY_HOST, got %q", got)
	}
}

func TestBuildServiceEnvVarsDefaultsGatewayHostToClusterScopedDNS(t *testing.T) {
	manifest := &inventory.Manifest{
		RootDomain: "frameworks.network",
		Clusters: map[string]inventory.ClusterConfig{
			"media-central-primary": {Name: "Media Central Primary"},
		},
		Services: map[string]inventory.ServiceConfig{
			"livepeer-gateway": {
				Enabled: true,
				Config: map[string]string{
					"network": "arbitrum-one-mainnet",
				},
			},
		},
	}

	env, err := buildServiceEnvVars(&orchestrator.Task{
		Name:      "livepeer-gateway",
		Type:      "livepeer-gateway",
		ClusterID: "media-central-primary",
	}, manifest, map[string]interface{}{}, "", "")
	if err != nil {
		t.Fatalf("buildServiceEnvVars returned error: %v", err)
	}

	if got := env["gateway_host"]; got != "livepeer.media-central-primary.frameworks.network" {
		t.Fatalf("expected cluster-scoped gateway_host, got %q", got)
	}
}

func TestBuildServiceEnvVarsRewritesGlobalGatewayHostToClusterScopedDNS(t *testing.T) {
	envFile := writeTestEnvFile(t, "LIVEPEER_GATEWAY_HOST=livepeer.frameworks.network\n")

	manifest := &inventory.Manifest{
		RootDomain: "frameworks.network",
		EnvFiles:   []string{envFile},
		Clusters: map[string]inventory.ClusterConfig{
			"media-central-primary": {Name: "Media Central Primary"},
		},
		Services: map[string]inventory.ServiceConfig{
			"livepeer-gateway": {
				Enabled: true,
			},
		},
	}

	env, err := buildServiceEnvVars(&orchestrator.Task{
		Name:      "livepeer-gateway",
		Type:      "livepeer-gateway",
		ClusterID: "media-central-primary",
	}, manifest, map[string]interface{}{}, "", "")
	if err != nil {
		t.Fatalf("buildServiceEnvVars returned error: %v", err)
	}

	if got := env["gateway_host"]; got != "livepeer.media-central-primary.frameworks.network" {
		t.Fatalf("expected cluster-scoped gateway_host, got %q", got)
	}
}

func writeTestEnvFile(t *testing.T, content string) string {
	t.Helper()

	path := filepath.Join(t.TempDir(), "service.env")
	if err := os.WriteFile(path, []byte(content), 0600); err != nil {
		t.Fatalf("write env file: %v", err)
	}
	return path
}

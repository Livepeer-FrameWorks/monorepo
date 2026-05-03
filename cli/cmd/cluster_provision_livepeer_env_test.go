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
		Profile:  "dev",
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
		Name:      "livepeer-gateway",
		Type:      "livepeer-gateway",
		ServiceID: "livepeer-gateway",
	}, manifest, map[string]interface{}{}, "", "", testLoadSharedEnv(t, manifest))
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
		Profile:  "dev",
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
		Name:      "livepeer-gateway",
		Type:      "livepeer-gateway",
		ServiceID: "livepeer-gateway",
	}, manifest, map[string]interface{}{}, "", "", testLoadSharedEnv(t, manifest))
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
		"LIVEPEER_AUTH_WEBHOOK_URL=https://auth.example\n")

	manifest := &inventory.Manifest{
		Profile:  "dev",
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
		Name:      "livepeer-gateway",
		Type:      "livepeer-gateway",
		ServiceID: "livepeer-gateway",
	}, manifest, map[string]interface{}{}, "", "", testLoadSharedEnv(t, manifest))
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
	if got := env["gateway_host"]; got != "" {
		t.Fatalf("gateway_host must not be populated from shared env aliases, got %q", got)
	}
}

func TestBuildServiceEnvVarsPreservesExplicitLivepeerGatewayHostConfig(t *testing.T) {
	manifest := &inventory.Manifest{
		Profile:    "dev",
		RootDomain: "frameworks.network",
		Clusters: map[string]inventory.ClusterConfig{
			"media-central-primary": {Name: "Media Central Primary"},
		},
		Services: map[string]inventory.ServiceConfig{
			"livepeer-gateway": {
				Enabled: true,
				Config: map[string]string{
					"gateway_host": "livepeer.manual.example",
				},
			},
		},
	}

	env, err := buildServiceEnvVars(&orchestrator.Task{
		Name:      "livepeer-gateway",
		Type:      "livepeer-gateway",
		ServiceID: "livepeer-gateway",
		ClusterID: "media-central-primary",
	}, manifest, map[string]interface{}{}, "", "", testLoadSharedEnv(t, manifest))
	if err != nil {
		t.Fatalf("buildServiceEnvVars returned error: %v", err)
	}

	if got := env["gateway_host"]; got != "livepeer.manual.example" {
		t.Fatalf("expected explicit gateway_host config to be preserved, got %q", got)
	}
}

func TestBuildServiceEnvVarsDoesNotDefaultGatewayHost(t *testing.T) {
	manifest := &inventory.Manifest{
		Profile:    "dev",
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
		ServiceID: "livepeer-gateway",
		ClusterID: "media-central-primary",
	}, manifest, map[string]interface{}{}, "", "", testLoadSharedEnv(t, manifest))
	if err != nil {
		t.Fatalf("buildServiceEnvVars returned error: %v", err)
	}

	if got := env["gateway_host"]; got != "" {
		t.Fatalf("gateway_host must not be auto-derived for an M:N gateway pool, got %q", got)
	}
}

func TestBuildServiceEnvVarsIgnoresSharedLivepeerGatewayHostAlias(t *testing.T) {
	envFile := writeTestEnvFile(t, "LIVEPEER_GATEWAY_HOST=livepeer.frameworks.network\n")

	manifest := &inventory.Manifest{
		Profile:    "dev",
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
		ServiceID: "livepeer-gateway",
		ClusterID: "media-central-primary",
	}, manifest, map[string]interface{}{}, "", "", testLoadSharedEnv(t, manifest))
	if err != nil {
		t.Fatalf("buildServiceEnvVars returned error: %v", err)
	}

	if got := env["gateway_host"]; got != "" {
		t.Fatalf("gateway_host must not be imported from shared env aliases, got %q", got)
	}
}

func TestBuildServiceEnvVarsSelectsLivepeerRPCPoolByGatewayHostOrder(t *testing.T) {
	envFile := writeTestEnvFile(t, ""+
		"LIVEPEER_ETH_URLS=https://rpc-one.example,https://rpc-two.example\n"+
		"LIVEPEER_ETH_ACCT_ADDR=0xabc123\n")

	manifest := &inventory.Manifest{
		Profile:    "production",
		RootDomain: "frameworks.network",
		EnvFiles:   []string{envFile},
		Clusters: map[string]inventory.ClusterConfig{
			"media-central-primary": {Name: "Media Central Primary"},
		},
		Services: map[string]inventory.ServiceConfig{
			"livepeer-gateway": {
				Enabled: true,
				Hosts:   []string{"gateway-a", "gateway-b"},
				Config: map[string]string{
					"network": "arbitrum-one-mainnet",
				},
			},
		},
	}

	env, err := buildServiceEnvVars(&orchestrator.Task{
		Name:      "livepeer-gateway@gateway-b",
		Type:      "livepeer-gateway",
		ServiceID: "livepeer-gateway",
		Host:      "gateway-b",
		ClusterID: "media-central-primary",
	}, manifest, map[string]interface{}{}, "", "", testLoadSharedEnv(t, manifest))
	if err != nil {
		t.Fatalf("buildServiceEnvVars returned error: %v", err)
	}

	if got := env["eth_url"]; got != "https://rpc-two.example" {
		t.Fatalf("expected second gateway to use second RPC URL, got %q", got)
	}
}

func TestBuildServiceEnvVarsLivepeerGatewayRuntimeDefaults(t *testing.T) {
	envFile := writeTestEnvFile(t, ""+
		"LIVEPEER_ETH_URLS=https://rpc-one.example\n"+
		"LIVEPEER_ETH_ACCT_ADDR=0xabc123\n")

	manifest := &inventory.Manifest{
		Profile:    "production",
		RootDomain: "frameworks.network",
		EnvFiles:   []string{envFile},
		Clusters: map[string]inventory.ClusterConfig{
			"core-central-primary": {Name: "Core Central Primary"},
		},
		Services: map[string]inventory.ServiceConfig{
			"livepeer-gateway": {
				Enabled: true,
				Host:    "central-eu-1",
			},
		},
	}

	env, err := buildServiceEnvVars(&orchestrator.Task{
		Name:      "livepeer-gateway",
		Type:      "livepeer-gateway",
		ServiceID: "livepeer-gateway",
		Host:      "central-eu-1",
		ClusterID: "core-central-primary",
	}, manifest, map[string]interface{}{}, "", "", testLoadSharedEnv(t, manifest))
	if err != nil {
		t.Fatalf("buildServiceEnvVars returned error: %v", err)
	}

	want := map[string]string{
		"network":                "arbitrum-one-mainnet",
		"http_addr":              "0.0.0.0:8935",
		"http_ingest":            "true",
		"cli_addr":               ":7935",
		"rtmp_addr":              "",
		"max_sessions":           "500",
		"max_price_per_unit":     "1200",
		"pixels_per_unit":        "1",
		"max_ticket_ev":          "3000000000000",
		"deposit_multiplier":     "1",
		"block_polling_interval": "20",
		"eth_url":                "https://rpc-one.example",
	}
	for key, wantValue := range want {
		if got := env[key]; got != wantValue {
			t.Fatalf("%s got %q, want %q", key, got, wantValue)
		}
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

// testLoadSharedEnv mimics the runProvision preload step so tests that
// previously relied on per-task env_file loading keep passing after the
// refactor that moved the load to the top of the provision run.
func testLoadSharedEnv(t *testing.T, m *inventory.Manifest) map[string]string {
	t.Helper()
	env, err := inventory.LoadSharedEnv(m, "", "")
	if err != nil {
		t.Fatalf("LoadSharedEnv: %v", err)
	}
	return env
}

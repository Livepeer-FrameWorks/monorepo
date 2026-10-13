package provisioner

import (
	"context"
	"testing"

	"frameworks/cli/pkg/inventory"
)

// Every service's task env starts from the whole shared and cluster env, so
// platform credentials such as the DNS provider token and LLM keys are present
// in it. Only what the process reads may reach its env file on the host.
var sharedPlatformCredentials = map[string]string{
	"CLOUDFLARE_API_TOKEN":       "cf-token",
	"SKIPPER_API_KEY":            "llm-key",
	"STRIPE_SECRET_KEY":          "stripe-key",
	"JWT_SECRET":                 "jwt-secret",
	"DATABASE_PASSWORD":          "owner-password",
	"GHCR_TOKEN":                 "registry-token",
	"BOSUN_FIELD_ENCRYPTION_KEY": "bosun-key",
}

func withSharedCredentials(env map[string]string) map[string]string {
	out := map[string]string{}
	for k, v := range sharedPlatformCredentials {
		out[k] = v
	}
	for k, v := range env {
		out[k] = v
	}
	return out
}

func assertNoSharedCredentials(t *testing.T, service string, env map[string]any, keep ...string) {
	t.Helper()
	kept := map[string]bool{}
	for _, k := range keep {
		kept[k] = true
	}
	for key := range sharedPlatformCredentials {
		if _, ok := env[key]; ok && !kept[key] {
			t.Errorf("%s env file carries %s, which the process never reads", service, key)
		}
	}
}

func TestLivepeerGatewayEnvFileCarriesOnlyGatewaySettings(t *testing.T) {
	vars, err := serviceNativeVars(context.Background(), genericServiceRoleConfig("livepeer-gateway", 8935), inventory.Host{Name: "media-1"}, ServiceConfig{
		Mode:      "native",
		Version:   "vtest",
		BinaryURL: "https://example.test/livepeer.tar.gz",
		EnvVars: withSharedCredentials(map[string]string{
			"network":                      "arbitrum-one-mainnet",
			"eth_url":                      "https://arb.example",
			"eth_acct_addr":                "0xabc123",
			"http_addr":                    "127.0.0.1:8935",
			"SERVICE_TOKEN":                "service-token",
			"GRPC_TLS_CA_PATH":             "/etc/frameworks/pki/ca.crt",
			"GEOIP_MMDB_PATH":              "/var/lib/GeoIP/city.mmdb",
			"FRAMEWORKS_DECKLOG_GRPC_ADDR": "decklog.internal:18006",
			"FRAMEWORKS_ENVIRONMENT":       "production",
		}),
		Metadata: map[string]any{"cluster_id": "media", "node_id": "media-1"},
	}, RoleBuildHelpers{})
	if err != nil {
		t.Fatalf("serviceNativeVars: %v", err)
	}
	env := vars["go_service_env"].(map[string]any)
	assertNoSharedCredentials(t, "livepeer-gateway", env)
	for _, key := range []string{"LP_ETHURL", "LP_DATADIR", "HOME", "SERVICE_TOKEN", "GRPC_TLS_CA_PATH", "GEOIP_MMDB_PATH", "FRAMEWORKS_DECKLOG_GRPC_ADDR", "FRAMEWORKS_ENVIRONMENT", "FRAMEWORKS_TELEMETRY_SPOOL_DIR"} {
		if _, ok := env[key]; !ok {
			t.Errorf("gateway env file lost %s", key)
		}
	}
	if _, ok := env["eth_url"]; ok {
		t.Error("gateway env file carries the raw eth_url input; go-livepeer reads it as LP_ETHURL")
	}
	if vars["go_service_livepeer_expected_wallet_address"] != "0xabc123" {
		t.Errorf("wallet address check input = %v", vars["go_service_livepeer_expected_wallet_address"])
	}
}

func TestPlatformServiceEnvFileCarriesOnlySchemaSettings(t *testing.T) {
	config := ServiceConfig{
		Mode:      "native",
		Version:   "vtest",
		BinaryURL: "https://example.test/commodore.tar.gz",
		EnvVars: withSharedCredentials(map[string]string{
			"DATABASE_URL":  "postgres://commodore@db/commodore",
			"SERVICE_TOKEN": "service-token",
			"BUILD_ENV":     "production",
			"LOG_LEVEL":     "info",
		}),
		Metadata: map[string]any{"cluster_id": "core", "node_id": "core-1"},
	}
	vars, err := serviceNativeVars(context.Background(), genericServiceRoleConfig("commodore", 18001), inventory.Host{Name: "core-1"}, config, RoleBuildHelpers{})
	if err != nil {
		t.Fatalf("serviceNativeVars: %v", err)
	}
	env := vars["go_service_env"].(map[string]any)
	// Commodore's typed configuration declares JWT_SECRET; the other
	// platform credentials belong to other services.
	assertNoSharedCredentials(t, "commodore", env, "JWT_SECRET")
	for _, key := range []string{"DATABASE_URL", "SERVICE_TOKEN", "JWT_SECRET", "BUILD_ENV", "LOG_LEVEL", "CLUSTER_ID", "NODE_ID"} {
		if _, ok := env[key]; !ok {
			t.Errorf("commodore env file lost %s", key)
		}
	}
	final := FinalServiceEnv("commodore", inventory.Host{Name: "core-1"}, config)
	if _, ok := final["CLOUDFLARE_API_TOKEN"]; ok {
		t.Error("the contract checks a commodore env that carries CLOUDFLARE_API_TOKEN, unlike the env file")
	}
}

func TestComposeDependencyEnvCarriesOnlyItsOwnSettings(t *testing.T) {
	vars, err := serviceComposeVars(context.Background(), genericServiceRoleConfig("metabase", 3001), inventory.Host{Name: "core-1"}, ServiceConfig{
		Mode:  "docker",
		Image: "metabase/metabase:v0.59.1",
		EnvVars: withSharedCredentials(map[string]string{
			"DATABASE_HOST": "10.0.0.5", "DATABASE_PORT": "5432", "DATABASE_NAME": "metabase", "DATABASE_USER": "metabase",
			"MB_SITE_URL": "https://metabase.example",
		}),
		Metadata: map[string]any{},
	}, RoleBuildHelpers{})
	if err != nil {
		t.Fatalf("serviceComposeVars: %v", err)
	}
	env := vars["compose_stack_env"].(map[string]any)
	assertNoSharedCredentials(t, "metabase", env)
	if env["MB_DB_PASS"] != "owner-password" || env["MB_SITE_URL"] != "https://metabase.example" || env["MB_DB_HOST"] != "10.0.0.5" {
		t.Errorf("metabase env lost its own settings: %v", env)
	}
}

func TestPrivateerEnvCarriesOnlyAgentSettings(t *testing.T) {
	env := FinalServiceEnv("privateer", inventory.Host{Name: "core-1", ExternalIP: "203.0.113.1"}, ServiceConfig{
		EnvVars: withSharedCredentials(map[string]string{
			"QUARTERMASTER_GRPC_ADDR": "10.0.0.2:19002",
			"SERVICE_TOKEN":           "service-token",
		}),
		Metadata: map[string]any{},
	})
	asAny := map[string]any{}
	for k, v := range env {
		asAny[k] = v
	}
	assertNoSharedCredentials(t, "privateer", asAny)
	for _, key := range []string{"QUARTERMASTER_GRPC_ADDR", "SERVICE_TOKEN", "MESH_NODE_NAME", "MESH_EXTERNAL_IP"} {
		if env[key] == "" {
			t.Errorf("privateer env lost %s", key)
		}
	}
}

package provisioner

import (
	"context"
	"testing"

	"frameworks/cli/pkg/inventory"
	"frameworks/pkg/datamigrate"
)

func TestServiceComposeVarsUsesSeparateContainerPortAndHealthPath(t *testing.T) {
	vars, err := serviceComposeVars(context.Background(), ServiceRoleConfig{
		ServiceName:   "metabase",
		DefaultPort:   3001,
		ContainerPort: 3000,
		HealthPath:    "/api/health",
		DefaultImage:  "metabase/metabase:v0.59.1",
	}, inventory.Host{Name: "central-eu-1"}, ServiceConfig{
		Mode:     "docker",
		Metadata: map[string]any{},
	}, RoleBuildHelpers{})
	if err != nil {
		t.Fatalf("serviceComposeVars: %v", err)
	}

	if got := vars["compose_stack_wait"]; got != false {
		t.Fatalf("compose_stack_wait got %v, want false", got)
	}
	service, ok := vars["compose_stack_service"].(map[string]any)
	if !ok {
		t.Fatalf("compose_stack_service got %T, want map[string]any", vars["compose_stack_service"])
	}
	if got := service["port"]; got != 3001 {
		t.Fatalf("port got %v, want 3001", got)
	}
	if got := service["container_port"]; got != 3000 {
		t.Fatalf("container_port got %v, want 3000", got)
	}
	if got := service["health_path"]; got != "/api/health" {
		t.Fatalf("health_path got %v, want /api/health", got)
	}
}

func TestServiceComposeVarsPassesRegistryAuthForGHCRImages(t *testing.T) {
	clearRegistryAuthEnv(t)

	vars, err := serviceComposeVars(context.Background(), ServiceRoleConfig{
		ServiceName:  "chartroom",
		DefaultPort:  18030,
		DefaultImage: "ghcr.io/livepeer-frameworks/frameworks-chartroom:latest",
	}, inventory.Host{Name: "regional-us-1"}, ServiceConfig{
		Mode: "docker",
		EnvVars: map[string]string{
			"GITHUB_ACTOR": "frameworks-bot",
			"GHCR_TOKEN":   "secret",
		},
		Metadata: map[string]any{},
	}, RoleBuildHelpers{})
	if err != nil {
		t.Fatalf("serviceComposeVars: %v", err)
	}

	auth, ok := vars["compose_stack_registry_auth"].(map[string]any)
	if !ok {
		t.Fatalf("compose_stack_registry_auth got %T, want map[string]any", vars["compose_stack_registry_auth"])
	}
	if got := auth["registry_url"]; got != "ghcr.io" {
		t.Fatalf("registry_url got %v, want ghcr.io", got)
	}
	if got := auth["username"]; got != "frameworks-bot" {
		t.Fatalf("username got %v, want frameworks-bot", got)
	}
	if got := auth["password"]; got != "secret" {
		t.Fatalf("password got %v, want secret", got)
	}
	if got := vars["compose_stack_require_registry_auth"]; got != true {
		t.Fatalf("compose_stack_require_registry_auth got %v, want true", got)
	}
}

func TestServiceComposeVarsOmitsIncompleteRegistryAuth(t *testing.T) {
	clearRegistryAuthEnv(t)

	vars, err := serviceComposeVars(context.Background(), ServiceRoleConfig{
		ServiceName:  "chartroom",
		DefaultPort:  18030,
		DefaultImage: "ghcr.io/livepeer-frameworks/frameworks-chartroom:latest",
	}, inventory.Host{Name: "regional-us-1"}, ServiceConfig{
		Mode: "docker",
		EnvVars: map[string]string{
			"GHCR_TOKEN": "secret",
		},
		Metadata: map[string]any{},
	}, RoleBuildHelpers{})
	if err != nil {
		t.Fatalf("serviceComposeVars: %v", err)
	}

	auth, ok := vars["compose_stack_registry_auth"].(map[string]any)
	if !ok {
		t.Fatalf("compose_stack_registry_auth got %T, want map[string]any", vars["compose_stack_registry_auth"])
	}
	if len(auth) != 0 {
		t.Fatalf("compose_stack_registry_auth got %v, want empty", auth)
	}
	if got := vars["compose_stack_require_registry_auth"]; got != true {
		t.Fatalf("compose_stack_require_registry_auth got %v, want true", got)
	}
}

func TestServiceComposeVarsInstallsDataMigrationsMarkerFromMetadata(t *testing.T) {
	vars, err := serviceComposeVars(context.Background(), ServiceRoleConfig{
		ServiceName:  "purser",
		DefaultPort:  18003,
		DefaultImage: "example/purser:test",
	}, inventory.Host{Name: "central-eu-1"}, ServiceConfig{
		Mode:     "docker",
		Metadata: map[string]any{"data_migrations": true},
	}, RoleBuildHelpers{})
	if err != nil {
		t.Fatalf("serviceComposeVars: %v", err)
	}

	if got := vars["compose_stack_data_migrations_marker"]; got != datamigrate.AdoptionMarkerPath("purser") {
		t.Fatalf("compose_stack_data_migrations_marker got %v", got)
	}
}

func TestServiceComposeVarsInstallsDataMigrationsMarkerAtDeployName(t *testing.T) {
	vars, err := serviceComposeVars(context.Background(), ServiceRoleConfig{
		ServiceName:  "purser",
		DefaultPort:  18003,
		DefaultImage: "example/purser:test",
	}, inventory.Host{Name: "central-eu-1"}, ServiceConfig{
		Mode:       "docker",
		DeployName: "billing-api",
		Metadata:   map[string]any{"data_migrations": true},
	}, RoleBuildHelpers{})
	if err != nil {
		t.Fatalf("serviceComposeVars: %v", err)
	}

	if got := vars["compose_stack_data_migrations_marker"]; got != datamigrate.AdoptionMarkerPath("billing-api") {
		t.Fatalf("compose_stack_data_migrations_marker got %v", got)
	}
}

func TestServiceComposeVarsAcceptsCommonGHCRPATNames(t *testing.T) {
	clearRegistryAuthEnv(t)

	vars, err := serviceComposeVars(context.Background(), ServiceRoleConfig{
		ServiceName:  "chartroom",
		DefaultPort:  18030,
		DefaultImage: "ghcr.io/livepeer-frameworks/frameworks-chartroom:latest",
	}, inventory.Host{Name: "regional-us-1"}, ServiceConfig{
		Mode: "docker",
		EnvVars: map[string]string{
			"GITHUB_USER": "frameworks-bot",
			"CR_PAT":      "secret",
		},
		Metadata: map[string]any{},
	}, RoleBuildHelpers{})
	if err != nil {
		t.Fatalf("serviceComposeVars: %v", err)
	}

	auth, ok := vars["compose_stack_registry_auth"].(map[string]any)
	if !ok {
		t.Fatalf("compose_stack_registry_auth got %T, want map[string]any", vars["compose_stack_registry_auth"])
	}
	if got := auth["registry_url"]; got != "ghcr.io" {
		t.Fatalf("registry_url got %v, want ghcr.io", got)
	}
	if got := auth["username"]; got != "frameworks-bot" {
		t.Fatalf("username got %v, want frameworks-bot", got)
	}
	if got := auth["password"]; got != "secret" {
		t.Fatalf("password got %v, want secret", got)
	}
}

func clearRegistryAuthEnv(t *testing.T) {
	t.Helper()
	for _, key := range []string{
		"DOCKER_REGISTRY",
		"CONTAINER_REGISTRY",
		"REGISTRY_URL",
		"REGISTRY_HOST",
		"DOCKER_USERNAME",
		"DOCKER_USER",
		"REGISTRY_USERNAME",
		"REGISTRY_USER",
		"GHCR_USERNAME",
		"GHCR_USER",
		"GHCR_OWNER",
		"GITHUB_ACTOR",
		"GITHUB_USERNAME",
		"GITHUB_USER",
		"DOCKER_PASSWORD",
		"DOCKER_TOKEN",
		"REGISTRY_PASSWORD",
		"REGISTRY_TOKEN",
		"GHCR_TOKEN",
		"GHCR_PAT",
		"CR_PAT",
		"GITHUB_TOKEN",
		"GITHUB_PAT",
		"GITHUB_PACKAGES_TOKEN",
		"PACKAGE_REGISTRY_TOKEN",
		"CONTAINER_REGISTRY_TOKEN",
		"REGISTRY_PAT",
	} {
		t.Setenv(key, "")
	}
}

func TestServiceNativeVarsIncludesRuntimePackages(t *testing.T) {
	vars, err := serviceNativeVars(context.Background(), ServiceRoleConfig{
		ServiceName:           "livepeer-gateway",
		DefaultPort:           8935,
		RuntimePackages:       []string{"common-runtime"},
		DebianRuntimePackages: []string{"libva-drm2"},
		PacmanRuntimePackages: []string{"libva"},
	}, inventory.Host{Name: "central-eu-1"}, ServiceConfig{
		Mode:      "native",
		Version:   "vtest",
		BinaryURL: "https://example.test/livepeer.tar.gz",
		Metadata:  map[string]any{},
	}, RoleBuildHelpers{})
	if err != nil {
		t.Fatalf("serviceNativeVars: %v", err)
	}

	assertStringSlice(t, vars["go_service_runtime_packages"], []string{"common-runtime"})
	assertStringSlice(t, vars["go_service_debian_runtime_packages"], []string{"libva-drm2"})
	assertStringSlice(t, vars["go_service_pacman_runtime_packages"], []string{"libva"})
}

func TestServiceNativeVarsDefaultsRuntimePackagesToEmptyLists(t *testing.T) {
	vars, err := serviceNativeVars(context.Background(), ServiceRoleConfig{
		ServiceName: "quartermaster",
		DefaultPort: 18002,
	}, inventory.Host{Name: "central-eu-1"}, ServiceConfig{
		Mode:      "native",
		Version:   "vtest",
		BinaryURL: "https://example.test/quartermaster.tar.gz",
		Metadata:  map[string]any{},
	}, RoleBuildHelpers{})
	if err != nil {
		t.Fatalf("serviceNativeVars: %v", err)
	}

	assertStringSlice(t, vars["go_service_runtime_packages"], []string{})
	assertStringSlice(t, vars["go_service_debian_runtime_packages"], []string{})
	assertStringSlice(t, vars["go_service_pacman_runtime_packages"], []string{})
}

func TestServiceNativeVarsBuildsLivepeerGatewayArgsAndStateDir(t *testing.T) {
	vars, err := serviceNativeVars(context.Background(), ServiceRoleConfig{
		ServiceName: "livepeer-gateway",
		DefaultPort: 8935,
		StateDirs:   []string{"/var/lib/frameworks/livepeer-gateway", "/var/lib/frameworks/livepeer-gateway/keystore"},
	}, inventory.Host{Name: "central-eu-1"}, ServiceConfig{
		Mode:      "native",
		Version:   "vtest",
		BinaryURL: "https://example.test/livepeer.tar.gz",
		EnvVars: map[string]string{
			"network":                "arbitrum-one-mainnet",
			"http_addr":              ":8935",
			"http_ingest":            "true",
			"cli_addr":               ":7935",
			"rtmp_addr":              "",
			"auth_webhook_url":       "http://foghorn.internal:18008/webhooks/livepeer/auth",
			"gateway_host":           "livepeer.media.example",
			"max_sessions":           "500",
			"max_price_per_unit":     "1200",
			"pixels_per_unit":        "1",
			"max_ticket_ev":          "3000000000000",
			"deposit_multiplier":     "1",
			"block_polling_interval": "20",
			"remote_signer_url":      "http://127.0.0.1:18016",
			"eth_url":                "https://arb.example",
			"eth_acct_addr":          "0xabc123",
			"orch_webhook_url":       "https://orch.example",
			"remote_discovery":       "true",
			"keystore_path":          "/etc/frameworks/livepeer-keystore",
		},
		Metadata: map[string]any{},
	}, RoleBuildHelpers{})
	if err != nil {
		t.Fatalf("serviceNativeVars: %v", err)
	}

	assertStringSlice(t, vars["go_service_state_dirs"], []string{"/var/lib/frameworks/livepeer-gateway", "/var/lib/frameworks/livepeer-gateway/keystore"})
	env, ok := vars["go_service_env"].(map[string]any)
	if !ok {
		t.Fatalf("go_service_env got %T, want map[string]any", vars["go_service_env"])
	}
	if got := env["LP_DATADIR"]; got != "/var/lib/frameworks/livepeer-gateway" {
		t.Fatalf("LP_DATADIR got %v", got)
	}
	if got := env["HOME"]; got != "/var/lib/frameworks/livepeer-gateway" {
		t.Fatalf("HOME got %v", got)
	}
	if got := vars["go_service_validate_timeout"]; got != 120 {
		t.Fatalf("go_service_validate_timeout got %v, want 120", got)
	}
	assertStringSlice(t, vars["go_service_args"], []string{
		"-gateway",
		"-dataDir=/var/lib/frameworks/livepeer-gateway",
		"-network=arbitrum-one-mainnet",
		"-httpAddr=:8935",
		"-httpIngest=true",
		"-cliAddr=:7935",
		"-rtmpAddr=",
		"-remoteSignerUrl=http://127.0.0.1:18016",
		"-authWebhookUrl=http://foghorn.internal:18008/webhooks/livepeer/auth",
		"-gatewayHost=livepeer.media.example",
		"-maxSessions=500",
		"-maxPricePerUnit=1200",
		"-pixelsPerUnit=1",
		"-maxTicketEV=3000000000000",
		"-depositMultiplier=1",
		"-blockPollingInterval=20",
		"-ethUrl=https://arb.example",
		"-ethAcctAddr=0xabc123",
		"-orchWebhookUrl=https://orch.example",
		"-remoteDiscovery=true",
		"-ethKeystorePath=/etc/frameworks/livepeer-keystore",
	})
}

func TestServiceNativeVarsMaterializesLivepeerKeystoreFiles(t *testing.T) {
	vars, err := serviceNativeVars(context.Background(), ServiceRoleConfig{
		ServiceName: "livepeer-gateway",
		DefaultPort: 8935,
		StateDirs:   []string{"/var/lib/frameworks/livepeer-gateway", "/var/lib/frameworks/livepeer-gateway/keystore"},
	}, inventory.Host{Name: "central-eu-1"}, ServiceConfig{
		Mode:      "native",
		Version:   "vtest",
		BinaryURL: "https://example.test/livepeer.tar.gz",
		EnvVars: map[string]string{
			"LIVEPEER_ETH_KEYSTORE_B64":      "eyJhZGRyZXNzIjoiMHhhYmMifQo=",
			"LIVEPEER_ETH_KEYSTORE_PASSWORD": "secret-password",
		},
		Metadata: map[string]any{},
	}, RoleBuildHelpers{})
	if err != nil {
		t.Fatalf("serviceNativeVars: %v", err)
	}

	assertStringSlice(t, vars["go_service_args"], []string{
		"-gateway",
		"-dataDir=/var/lib/frameworks/livepeer-gateway",
		"-ethKeystorePath=/var/lib/frameworks/livepeer-gateway/keystore/key.json",
		"-ethPassword=/var/lib/frameworks/livepeer-gateway/eth-password",
	})

	files, ok := vars["go_service_files"].([]map[string]string)
	if !ok {
		t.Fatalf("go_service_files got %T, want []map[string]string", vars["go_service_files"])
	}
	if len(files) != 2 {
		t.Fatalf("go_service_files got %v, want 2 files", files)
	}
	if files[0]["path"] != "/var/lib/frameworks/livepeer-gateway/keystore/key.json" {
		t.Fatalf("unexpected keystore path: %v", files[0])
	}
	if files[0]["content"] != "{\"address\":\"0xabc\"}\n" {
		t.Fatalf("unexpected keystore content: %q", files[0]["content"])
	}
	if files[1]["path"] != "/var/lib/frameworks/livepeer-gateway/eth-password" {
		t.Fatalf("unexpected password path: %v", files[1])
	}
	if files[1]["content"] != "secret-password" {
		t.Fatalf("unexpected password content: %q", files[1]["content"])
	}
	env, ok := vars["go_service_env"].(map[string]any)
	if !ok {
		t.Fatalf("go_service_env got %T, want map[string]any", vars["go_service_env"])
	}
	if _, ok := env["LIVEPEER_ETH_KEYSTORE_B64"]; ok {
		t.Fatal("LIVEPEER_ETH_KEYSTORE_B64 should not remain in service env")
	}
	if _, ok := env["LIVEPEER_ETH_KEYSTORE_PASSWORD"]; ok {
		t.Fatal("LIVEPEER_ETH_KEYSTORE_PASSWORD should not remain in service env")
	}
	if got := vars["go_service_livepeer_expected_keystore_path"]; got != "/var/lib/frameworks/livepeer-gateway/keystore/key.json" {
		t.Fatalf("unexpected expected keystore path: %v", got)
	}
	if got := vars["go_service_livepeer_expected_keystore_dir"]; got != "/var/lib/frameworks/livepeer-gateway/keystore" {
		t.Fatalf("unexpected expected keystore dir: %v", got)
	}
}

func assertStringSlice(t *testing.T, got any, want []string) {
	t.Helper()
	gotSlice, ok := got.([]string)
	if !ok {
		t.Fatalf("got %T, want []string", got)
	}
	if len(gotSlice) != len(want) {
		t.Fatalf("got %v, want %v", gotSlice, want)
	}
	for i := range want {
		if gotSlice[i] != want[i] {
			t.Fatalf("got %v, want %v", gotSlice, want)
		}
	}
}

package provisioner

import (
	"context"
	"testing"

	"frameworks/cli/pkg/inventory"
)

func mockPrivateerHelpers() RoleBuildHelpers {
	return RoleBuildHelpers{
		DetectRemoteOS: func(ctx context.Context, host inventory.Host) (string, string, error) {
			return "linux", "amd64", nil
		},
		ResolveArtifact: func(name, arch, channel string, metadata map[string]any) (ResolvedArtifact, error) {
			return ResolvedArtifact{URL: "u", Checksum: "c", Version: "1.2.3", Arch: arch}, nil
		},
	}
}

func TestPrivateerRoleVarsCopiesEnvVarsIntoPrivateerEnv(t *testing.T) {
	config := ServiceConfig{
		EnvVars: map[string]string{
			"SERVICE_TOKEN":           "svc-tok",
			"QUARTERMASTER_GRPC_ADDR": "10.88.0.1:19002",
		},
	}
	vars, err := privateerRoleVars(context.Background(), inventory.Host{}, config, mockPrivateerHelpers())
	if err != nil {
		t.Fatalf("privateerRoleVars: %v", err)
	}
	env, ok := vars["privateer_env"].(map[string]any)
	if !ok {
		t.Fatalf("privateer_env missing or wrong type: %T", vars["privateer_env"])
	}
	if env["SERVICE_TOKEN"] != "svc-tok" {
		t.Errorf("SERVICE_TOKEN = %v, want svc-tok", env["SERVICE_TOKEN"])
	}
	if env["QUARTERMASTER_GRPC_ADDR"] != "10.88.0.1:19002" {
		t.Errorf("QUARTERMASTER_GRPC_ADDR = %v, want 10.88.0.1:19002", env["QUARTERMASTER_GRPC_ADDR"])
	}
}

func TestPrivateerRoleVarsInjectsBootstrapRuntimeEnv(t *testing.T) {
	host := inventory.Host{
		Name:       "core-1",
		ExternalIP: "203.0.113.10",
		Roles:      []string{"control"},
	}
	config := ServiceConfig{
		Metadata: map[string]any{
			"wireguard_ip":                    "10.88.0.2",
			"wireguard_port":                  51900,
			"wireguard_private_key":           "priv",
			"static_peers":                    []map[string]any{{"name": "core-2"}},
			"expected_internal_grpc_services": []string{"commodore", "navigator"},
		},
	}
	vars, err := privateerRoleVars(context.Background(), host, config, mockPrivateerHelpers())
	if err != nil {
		t.Fatalf("privateerRoleVars: %v", err)
	}
	env := vars["privateer_env"].(map[string]any)
	for key, want := range map[string]any{
		"MESH_NODE_NAME":                  "core-1",
		"MESH_NODE_TYPE":                  "core",
		"MESH_EXTERNAL_IP":                "203.0.113.10",
		"MESH_WIREGUARD_IP":               "10.88.0.2",
		"MESH_LISTEN_PORT":                "51900",
		"MESH_PRIVATE_KEY_FILE":           "/etc/privateer/wg.key",
		"PRIVATEER_STATIC_PEERS_FILE":     "/etc/privateer/static-peers.json",
		"PRIVATEER_DATA_DIR":              "/var/lib/privateer",
		"EXPECTED_INTERNAL_GRPC_SERVICES": "commodore,navigator",
	} {
		if env[key] != want {
			t.Errorf("%s = %v, want %v", key, env[key], want)
		}
	}
}

func TestPrivateerRoleVarsKeepsSeedFileConfiguredWithZeroPeers(t *testing.T) {
	host := inventory.Host{Name: "solo-1"}
	config := ServiceConfig{
		Metadata: map[string]any{
			"static_peers": []map[string]any{},
		},
	}
	vars, err := privateerRoleVars(context.Background(), host, config, mockPrivateerHelpers())
	if err != nil {
		t.Fatalf("privateerRoleVars: %v", err)
	}
	env := vars["privateer_env"].(map[string]any)
	if env["PRIVATEER_STATIC_PEERS_FILE"] != "/etc/privateer/static-peers.json" {
		t.Fatalf("PRIVATEER_STATIC_PEERS_FILE = %v, want /etc/privateer/static-peers.json", env["PRIVATEER_STATIC_PEERS_FILE"])
	}
	peers, ok := vars["privateer_static_peers"].([]map[string]any)
	if !ok {
		t.Fatalf("privateer_static_peers missing or wrong type: %T", vars["privateer_static_peers"])
	}
	if len(peers) != 0 {
		t.Fatalf("privateer_static_peers len = %d, want 0", len(peers))
	}
}

func TestPrivateerRoleVarsMetadataEnvOverridesEnvVars(t *testing.T) {
	config := ServiceConfig{
		EnvVars: map[string]string{"FOO": "from-envvars"},
		Metadata: map[string]any{
			"env": map[string]string{"FOO": "from-metadata"},
		},
	}
	vars, err := privateerRoleVars(context.Background(), inventory.Host{}, config, mockPrivateerHelpers())
	if err != nil {
		t.Fatalf("privateerRoleVars: %v", err)
	}
	env := vars["privateer_env"].(map[string]any)
	if env["FOO"] != "from-metadata" {
		t.Errorf("FOO = %v, want from-metadata", env["FOO"])
	}
}

func TestPrivateerRoleVarsAlwaysIncludesRuntimeIdentityEnv(t *testing.T) {
	config := ServiceConfig{}
	vars, err := privateerRoleVars(context.Background(), inventory.Host{}, config, mockPrivateerHelpers())
	if err != nil {
		t.Fatalf("privateerRoleVars: %v", err)
	}
	env, ok := vars["privateer_env"].(map[string]any)
	if !ok {
		t.Fatalf("privateer_env missing or wrong type: %T", vars["privateer_env"])
	}
	if env["MESH_NODE_TYPE"] != "core" {
		t.Errorf("MESH_NODE_TYPE = %v, want core", env["MESH_NODE_TYPE"])
	}
	if env["PRIVATEER_DATA_DIR"] != "/var/lib/privateer" {
		t.Errorf("PRIVATEER_DATA_DIR = %v, want /var/lib/privateer", env["PRIVATEER_DATA_DIR"])
	}
	if env["PRIVATEER_STATIC_PEERS_FILE"] != "/etc/privateer/static-peers.json" {
		t.Errorf("PRIVATEER_STATIC_PEERS_FILE = %v, want /etc/privateer/static-peers.json", env["PRIVATEER_STATIC_PEERS_FILE"])
	}
}

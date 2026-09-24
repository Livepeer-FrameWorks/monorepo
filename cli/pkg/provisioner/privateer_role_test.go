package provisioner

import (
	"context"
	"strings"
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

// The .internal route is link-scoped on wg0 and must be re-applied whenever wg0
// appears or Privateer restarts; a once-at-boot oneshot leaves .internal
// lookups to the LAN resolver after the link loses its settings. The global
// resolved scope must not point at Privateer, which answers non-.internal names
// with SERVFAIL when UPSTREAM_DNS is unset.
func TestPrivateerDNSSocketOutlivesServiceRestarts(t *testing.T) {
	const role = "ansible/collections/ansible_collections/frameworks/infra/roles/privateer/"
	socket := readRepoFile(t, role+"templates/privateer.socket.j2")
	for _, want := range []string{
		"ListenStream=127.0.0.1:{{ privateer_dns_port }}\n",
		"ListenDatagram=127.0.0.1:{{ privateer_dns_port }}\n",
		"WantedBy=sockets.target\n",
	} {
		if !strings.Contains(socket, want) {
			t.Errorf("privateer socket unit missing %q:\n%s", want, socket)
		}
	}
	// PartOf/BindsTo on the socket would tear it down with every service
	// restart, which is exactly the DNS gap the socket exists to close.
	for _, forbidden := range []string{"PartOf=", "BindsTo="} {
		if strings.Contains(socket, forbidden) {
			t.Errorf("privateer socket unit must not follow the service lifecycle; found %q", forbidden)
		}
	}
	if vars := readRepoFile(t, role+"vars/main.yml"); !strings.Contains(vars, `privateer_dns_port: "{{ privateer_env.DNS_PORT | default(53) }}"`) {
		t.Errorf("privateer_dns_port must follow Privateer's DNS_PORT:\n%s", vars)
	}

	handlers := readRepoFile(t, role+"handlers/main.yml")
	if strings.Contains(handlers, "frameworks-privateer.socket") {
		t.Errorf("privateer restart handlers must leave the DNS socket open:\n%s", handlers)
	}

	service := readRepoFile(t, role+"tasks/service.yml")
	stop := strings.Index(service, "- name: Stop privateer so the DNS socket unit can bind")
	socketStart := strings.Index(service, "- name: Ensure Privateer DNS socket is enabled and listening")
	serviceStart := strings.Index(service, "- name: Ensure privateer is enabled and running")
	if stop < 0 || socketStart < 0 || serviceStart < 0 || stop >= socketStart || socketStart >= serviceStart {
		t.Fatalf("service.yml must stop Privateer, bind the socket, then start Privateer (stop=%d socket=%d service=%d):\n%s", stop, socketStart, serviceStart, service)
	}
	stopTask := service[stop:socketStart]
	for _, want := range []string{
		"privateer_dns_socket_state.stdout | default('') != 'active'",
		"privateer_dns_socket_unit.changed | default(false)",
	} {
		if !strings.Contains(stopTask, want) {
			t.Errorf("Privateer must only be stopped when the socket needs (re)binding; missing %q:\n%s", want, stopTask)
		}
	}

	cleanup := readRepoFile(t, role+"tasks/cleanup.yml")
	if socketStop, serviceStop := strings.Index(cleanup, "name: frameworks-privateer.socket"), strings.Index(cleanup, "name: frameworks-privateer\n"); socketStop < 0 || serviceStop < 0 || socketStop > serviceStop {
		t.Errorf("cleanup must stop the DNS socket before the service, or a query re-activates it:\n%s", cleanup)
	}
}

func TestPrivateerInternalRouteFollowsWireGuardLink(t *testing.T) {
	configure := readRepoFile(t, "ansible/collections/ansible_collections/frameworks/infra/roles/privateer/tasks/configure.yml")
	for _, want := range []string{
		"BindsTo=sys-subsystem-net-devices-wg0.device\n",
		"After=sys-subsystem-net-devices-wg0.device systemd-resolved.service frameworks-privateer.service\n",
		"PartOf=frameworks-privateer.service\n",
		"WantedBy=sys-subsystem-net-devices-wg0.device\n",
		" dns wg0 127.0.0.1\n",
		" domain wg0 ~internal\n",
		"- reenable\n",
	} {
		if !strings.Contains(configure, want) {
			t.Errorf("privateer configure.yml missing %q", want)
		}
	}
	if strings.Contains(configure, "WantedBy=multi-user.target") {
		t.Error("route unit still starts once from multi-user.target")
	}
	dropIn := configure[strings.Index(configure, "dest: /etc/systemd/resolved.conf.d/frameworks-privateer.conf"):]
	dropIn = dropIn[:strings.Index(dropIn, "register:")]
	if strings.Contains(dropIn, "DNS=") || strings.Contains(dropIn, "Domains=") {
		t.Errorf("global resolved drop-in routes names to Privateer:\n%s", dropIn)
	}
}

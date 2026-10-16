package provisioner

import (
	"context"
	"encoding/base64"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"testing"

	"frameworks/cli/pkg/inventory"
)

// Every listener a platform host runs binds loopback and the host's WireGuard
// address, or loopback alone; only the edge media ports, the reverse proxy's
// 80/443 and Foghorn's external gRPC listener are public. A host without a
// WireGuard address is a single-host cluster and binds loopback. These tests
// pin each infrastructure role's bind to that policy.

var (
	meshHost   = inventory.Host{Name: "core-1", WireguardIP: "10.88.0.4"}
	singleHost = inventory.Host{Name: "single"}
)

func TestPostgresListensOnLoopbackAndMesh(t *testing.T) {
	render := func(host inventory.Host, metadata map[string]any) map[string]any {
		t.Helper()
		metadata["postgres_password"] = "secret"
		vars, err := postgresRoleVars(context.Background(), host, ServiceConfig{Version: "18.4", Metadata: metadata}, exporterTestHelpers(t, map[string]bool{}))
		if err != nil {
			t.Fatalf("postgresRoleVars: %v", err)
		}
		return vars
	}
	vars := render(meshHost, map[string]any{"mesh_cidr": "10.88.0.0/16"})
	if got := vars["postgres_listen_addresses"]; got != "127.0.0.1,10.88.0.4" {
		t.Fatalf("postgres_listen_addresses = %v, want 127.0.0.1,10.88.0.4", got)
	}
	// Services on this host and on mesh peers dial <host>.internal, the
	// WireGuard address, and authenticate with a password from there.
	if got := vars["postgres_hba_mesh_cidr"]; got != "10.88.0.0/16" {
		t.Fatalf("postgres_hba_mesh_cidr = %v, want the mesh CIDR", got)
	}
	single := render(singleHost, map[string]any{})
	if got := single["postgres_listen_addresses"]; got != "127.0.0.1" {
		t.Fatalf("postgres_listen_addresses without mesh = %v, want 127.0.0.1", got)
	}
	if _, ok := single["postgres_hba_mesh_cidr"]; ok {
		t.Fatal("a host without a WireGuard address got a mesh HBA entry")
	}
	install := readRepoFile(t, "ansible/collections/ansible_collections/frameworks/infra/roles/postgres/tasks/install.yml")
	if !strings.Contains(install, "'address': postgres_hba_mesh_cidr, 'auth_method': 'scram-sha-256'") {
		t.Fatalf("postgres role does not render the mesh HBA entry:\n%s", install)
	}
	if defaults := readRepoFile(t, "ansible/collections/ansible_collections/frameworks/infra/roles/postgres/defaults/main.yml"); strings.Contains(defaults, `postgres_listen_addresses: "*"`) {
		t.Fatal("postgres role default listens on every interface")
	}
}

func TestKafkaListenersBindTheMeshAddress(t *testing.T) {
	helpers := RoleBuildHelpers{
		DetectRemoteOS: func(context.Context, inventory.Host) (string, string, error) { return "linux", "amd64", nil },
		ResolveArtifact: func(string, string, string, map[string]any) (ResolvedArtifact, error) {
			return ResolvedArtifact{URL: "https://example.test/kafka.tgz", Checksum: "sha512:aa", Version: "4.2.0"}, nil
		},
	}
	for _, role := range []string{"broker", "combined"} {
		vars, err := kafkaRoleVarsFor(role)(context.Background(), meshHost, ServiceConfig{Metadata: map[string]any{"advertised_host": "10.88.0.4"}}, helpers)
		if err != nil {
			t.Fatalf("kafka %s vars: %v", role, err)
		}
		if got := vars["kafka_bind_host"]; got != "10.88.0.4" {
			t.Fatalf("kafka %s bind host = %v, want the WireGuard address", role, got)
		}
		single, err := kafkaRoleVarsFor(role)(context.Background(), singleHost, ServiceConfig{Metadata: map[string]any{"advertised_host": "single"}}, helpers)
		if err != nil {
			t.Fatalf("kafka %s vars without mesh: %v", role, err)
		}
		if single["kafka_bind_host"] != "127.0.0.1" || single["kafka_advertised_host"] != "127.0.0.1" {
			t.Fatalf("kafka %s without mesh binds %v advertising %v, want loopback for both", role, single["kafka_bind_host"], single["kafka_advertised_host"])
		}
	}
	role := "ansible/collections/ansible_collections/frameworks/infra/roles/kafka/"
	for _, file := range []string{"tasks/init.yml", "tasks/validate.yml", "tasks/restart.yml"} {
		if content := readRepoFile(t, role+file); strings.Contains(content, "127.0.0.1") {
			t.Fatalf("kafka %s dials loopback, where a WireGuard-bound broker does not listen:\n%s", file, content)
		}
	}
}

func TestYugabyteListenersBindTheNodeAddress(t *testing.T) {
	vars, err := yugabyteRoleVars(context.Background(), meshHost, ServiceConfig{Metadata: map[string]any{"advertised_host": "10.88.0.4", "master_addresses": "10.88.0.4:7100", "mesh_cidr": "10.88.0.0/16"}}, mockPrivateerHelpers())
	if err != nil {
		t.Fatal(err)
	}
	if got := vars["yugabyte_node_address"]; got != "10.88.0.4" {
		t.Fatalf("yugabyte_node_address = %v, want the WireGuard address", got)
	}
	// YSQL accepts password logins from the mesh only, never 0.0.0.0/0.
	if got := vars["yugabyte_hba_client_cidr"]; got != "10.88.0.0/16" {
		t.Fatalf("yugabyte_hba_client_cidr = %v, want the mesh CIDR", got)
	}
	single, err := yugabyteRoleVars(context.Background(), inventory.Host{Name: "single", ExternalIP: "203.0.113.9"}, ServiceConfig{Metadata: map[string]any{"advertised_host": "single", "master_addresses": "single:7100"}}, mockPrivateerHelpers())
	if err != nil {
		t.Fatal(err)
	}
	if single["yugabyte_node_address"] != "127.0.0.1" || single["yugabyte_master_addresses"] != "127.0.0.1:7100" {
		t.Fatalf("yugabyte without mesh binds %v with masters %v, want loopback", single["yugabyte_node_address"], single["yugabyte_master_addresses"])
	}
	tserver := readRepoFile(t, "ansible/collections/ansible_collections/frameworks/infra/roles/yugabyte/templates/tserver.conf.j2")
	for _, want := range []string{
		"--pgsql_proxy_bind_address={{ yugabyte_node_address }}:{{ yugabyte_ysql_port }}",
		"--cql_proxy_bind_address=127.0.0.1:{{ yugabyte_ycql_port }}",
		"host all {{ yugabyte_superuser_role }} {{ yugabyte_node_address }}/32 trust,host all all {{ yugabyte_hba_client_cidr }} scram-sha-256",
	} {
		if !strings.Contains(tserver, want) {
			t.Fatalf("tserver.conf.j2 lacks %q:\n%s", want, tserver)
		}
	}
	if strings.Contains(tserver, "0.0.0.0:") {
		t.Fatalf("tserver.conf.j2 binds a listener on every interface:\n%s", tserver)
	}
	tasks, err := filepath.Glob(filepath.Join("../../..", "ansible/collections/ansible_collections/frameworks/infra/roles/yugabyte/tasks/*.yml"))
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range tasks {
		content, readErr := os.ReadFile(path)
		if readErr != nil {
			t.Fatal(readErr)
		}
		if regexp.MustCompile(`login_host: 127\.0\.0\.1|-h 127\.0\.0\.1`).Match(content) {
			t.Fatalf("%s connects YSQL to loopback, where the tserver no longer listens", filepath.Base(path))
		}
	}
}

// fw_ysql_host is the -h every node-local YSQL client uses: the address the
// running listener binds, or loopback while an older tserver still binds
// every interface and trusts the superuser only from 127.0.0.1.
func TestFwYSQLHostFollowsTheRunningListener(t *testing.T) {
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash not available")
	}
	for listener, want := range map[string]string{
		"LISTEN 0 128 10.88.0.4:5433 0.0.0.0:*": "10.88.0.4",
		"LISTEN 0 128 0.0.0.0:5433 0.0.0.0:*":   "127.0.0.1",
		"LISTEN 0 128 *:5433 *:*":               "127.0.0.1",
		"LISTEN 0 128 [fd00::4]:5433 [::]:*":    "fd00::4",
		"":                                      "127.0.0.1",
	} {
		dir := t.TempDir()
		stub := "#!/bin/sh\nprintf '%s\\n' '" + listener + "'\n"
		if listener == "" {
			stub = "#!/bin/sh\nexit 0\n"
		}
		if err := os.WriteFile(filepath.Join(dir, "ss"), []byte(stub), 0o755); err != nil {
			t.Fatal(err)
		}
		cmd := exec.CommandContext(t.Context(), "bash", "-c", YugabyteBinaryResolverShell+"\nfw_ysql_host 5433")
		cmd.Env = append(os.Environ(), "PATH="+dir+":"+os.Getenv("PATH"))
		out, err := cmd.Output()
		if err != nil {
			t.Fatalf("fw_ysql_host with %q: %v", listener, err)
		}
		if got := strings.TrimSpace(string(out)); got != want {
			t.Fatalf("fw_ysql_host with listener %q = %q, want %q", listener, got, want)
		}
	}
}

func TestClickHouseKeeperRaftBindsListenHosts(t *testing.T) {
	keeper := readRepoFile(t, "ansible/collections/ansible_collections/frameworks/infra/roles/clickhouse/templates/keeper_config.xml.j2")
	// Keeper's Raft port ignores listen_host and binds every interface unless
	// interserver_listen_host names its addresses.
	if !strings.Contains(keeper, "<interserver_listen_host>{{ listen_host }}</interserver_listen_host>") {
		t.Fatalf("keeper config does not bind the Raft port to the listen hosts:\n%s", keeper)
	}
}

func TestMetricsStackListenersBindLoopbackAndMesh(t *testing.T) {
	vars, err := prometheusStackRoleVars(context.Background(), meshHost, ServiceConfig{Metadata: map[string]any{"component": "victoriametrics"}}, alertingTestHelpers(t, "victoriametrics"))
	if err != nil {
		t.Fatal(err)
	}
	if got := vars["prometheus_stack_listen_addrs"]; !reflect.DeepEqual(got, []string{"127.0.0.1", "10.88.0.4"}) {
		t.Fatalf("prometheus_stack_listen_addrs = %#v, want loopback and the WireGuard address", got)
	}
	role := "ansible/collections/ansible_collections/frameworks/infra/roles/prometheus_stack/"
	for file, want := range map[string]string{
		"templates/victoriametrics.service.j2": "-httpListenAddr={{ addr }}:{{ victoriametrics_port }}",
		"templates/vmauth.service.j2":          "-httpListenAddr={{ addr }}:{{ vmauth_port }}",
		"templates/alertmanager.service.j2":    "--web.listen-address={{ addr }}:{{ alertmanager_port }}",
		"templates/vmagent.service.j2":         "-httpListenAddr=127.0.0.1:{{ vmagent_port }}",
		"templates/vmalert.service.j2":         "'-httpListenAddr=127.0.0.1:' ~ vmalert_port",
		"tasks/prometheus_community.yml":       `prometheus_web_listen_address: "127.0.0.1:{{ prometheus_port }}"`,
	} {
		content := readRepoFile(t, role+file)
		if !strings.Contains(content, want) {
			t.Fatalf("%s lacks %q:\n%s", file, want, content)
		}
		if regexp.MustCompile(`(httpListenAddr|listen-address|listen_address)[=:]"?(0\.0\.0\.0)?:\{\{`).MatchString(content) {
			t.Fatalf("%s binds a listener on every interface:\n%s", file, content)
		}
	}
}

func TestNginxPlainHTTPPortIsLoopbackOnly(t *testing.T) {
	conf := readRepoFile(t, "ansible/collections/ansible_collections/frameworks/infra/roles/nginx/templates/frameworks.conf.j2")
	// The plain-HTTP copy of every site skips TLS and the HTTPS redirect.
	if strings.Contains(conf, "listen {{ nginx_http_port }}") {
		t.Fatalf("nginx serves its plain-HTTP site copies on every interface:\n%s", conf)
	}
	defaults := readRepoFile(t, "ansible/collections/ansible_collections/frameworks/infra/roles/nginx/defaults/main.yml")
	if !strings.Contains(defaults, "nginx_http_bind_address: 127.0.0.1") {
		t.Fatalf("nginx_http_bind_address default is not loopback:\n%s", defaults)
	}
}

func TestPrivateerListensOnLoopbackAndMesh(t *testing.T) {
	env := privateerEnv(meshHost, ServiceConfig{Metadata: map[string]any{}})
	if got := env["BIND_ADDRS"]; got != "127.0.0.1,10.88.0.4" {
		t.Fatalf("privateer BIND_ADDRS = %q, want 127.0.0.1,10.88.0.4", got)
	}
	configure := readRepoFile(t, "ansible/collections/ansible_collections/frameworks/infra/roles/privateer/tasks/configure.yml")
	if !strings.Contains(configure, "net.ipv4.ip_nonlocal_bind = 1") {
		t.Fatal("privateer role does not let services bind the mesh address before wg0 is up")
	}
}

// A docker-mode go-livepeer service gets its flags as LP_* variables, binds
// every container interface on its configured ports, publishes http_addr on
// loopback and the WireGuard address and cli_addr on loopback, and reads its
// keystore from a 0600 project file.
func TestLivepeerGatewayComposeBindsContainerAndPublishesLoopbackAndMesh(t *testing.T) {
	cfg := genericServiceRoleConfig("livepeer-gateway", 8935)
	cfg.DefaultImage = "livepeerframeworks/go-livepeer:test"
	vars, err := serviceComposeVars(context.Background(), cfg, meshHost, ServiceConfig{
		Mode: "docker",
		EnvVars: map[string]string{
			"network":                        "arbitrum-one-mainnet",
			"http_addr":                      "127.0.0.1:8935",
			"cli_addr":                       "127.0.0.1:7935",
			"rtmp_addr":                      "127.0.0.1:1936",
			"http_ingest":                    "true",
			"eth_acct_addr":                  "0xabc",
			"auth_webhook_url":               "http://foghorn.internal:18008/webhooks/livepeer/auth",
			"LIVEPEER_ETH_KEYSTORE_B64":      base64.StdEncoding.EncodeToString([]byte(`{"address":"abc"}`)),
			"LIVEPEER_ETH_KEYSTORE_PASSWORD": "pw",
		},
		Metadata: map[string]any{},
	}, RoleBuildHelpers{})
	if err != nil {
		t.Fatalf("serviceComposeVars: %v", err)
	}
	env := vars["compose_stack_env"].(map[string]any)
	for key, want := range map[string]string{
		"LP_GATEWAY":         "true",
		"LP_HTTPADDR":        "0.0.0.0:8935",
		"LP_CLIADDR":         "0.0.0.0:7935",
		"LP_RTMPADDR":        "127.0.0.1:1936",
		"LP_NETWORK":         "arbitrum-one-mainnet",
		"LP_ETHKEYSTOREPATH": "/var/lib/frameworks/livepeer-gateway/keystore/key.json",
		"LP_ETHPASSWORD":     "pw",
		"LP_AUTHWEBHOOKURL":  "http://foghorn.internal:18008/webhooks/livepeer/auth",
	} {
		if got := env[key]; got != want {
			t.Fatalf("%s = %v, want %q", key, got, want)
		}
	}
	for key := range env {
		if key == "http_addr" || key == "LIVEPEER_ETH_KEYSTORE_B64" || key == "LIVEPEER_ETH_KEYSTORE_PASSWORD" {
			t.Fatalf("compose env carries %s", key)
		}
	}
	service := vars["compose_stack_service"].(map[string]any)
	if got := composePortSpecs(t, service); !reflect.DeepEqual(got, []string{"127.0.0.1:8935:8935", "10.88.0.4:8935:8935", "127.0.0.1:7935:7935"}) {
		t.Fatalf("gateway ports = %#v", got)
	}
	volumes := service["volumes"].([]string)
	if !slicesContains(volumes, "./keystore/key.json:/var/lib/frameworks/livepeer-gateway/keystore/key.json:ro") ||
		!slicesContains(volumes, "/var/lib/frameworks/livepeer-gateway:/var/lib/frameworks/livepeer-gateway") {
		t.Fatalf("gateway volumes = %#v", volumes)
	}
	secrets := vars["compose_stack_secret_files"].(map[string]string)
	if decoded, _ := base64.StdEncoding.DecodeString(secrets["keystore/key.json"]); string(decoded) != `{"address":"abc"}` {
		t.Fatalf("keystore secret file = %q", decoded)
	}
	install := readRepoFile(t, "ansible/collections/ansible_collections/frameworks/infra/roles/compose_stack/tasks/install.yml")
	if !strings.Contains(install, "compose_stack_secret_files | dict2items") || !strings.Contains(install, `mode: "0600"`) {
		t.Fatal("compose_stack does not write secret files with mode 0600")
	}
}

// A signer's http_addr binds the host's WireGuard address alone, so the
// native rollout gate probes it there instead of on loopback.
func TestLivepeerSignerNativeGateProbesItsHTTPAddr(t *testing.T) {
	cfg := genericServiceRoleConfig("livepeer-signer", 18016)
	vars, err := serviceNativeVars(context.Background(), cfg, meshHost, ServiceConfig{
		Mode:      "native",
		BinaryURL: "http://example.invalid/livepeer.tar.gz",
		EnvVars:   map[string]string{"http_addr": "10.88.0.4:18016", "cli_addr": "127.0.0.1:3935"},
		Metadata:  map[string]any{},
	}, RoleBuildHelpers{})
	if err != nil {
		t.Fatalf("serviceNativeVars: %v", err)
	}
	if got := vars["go_service_health_host"]; got != "10.88.0.4" {
		t.Fatalf("go_service_health_host = %v, want the signer's http_addr host", got)
	}
	validate := readRepoFile(t, "ansible/collections/ansible_collections/frameworks/infra/roles/go_service/tasks/validate.yml")
	if strings.Contains(validate, "127.0.0.1") {
		t.Fatal("go_service validate still probes loopback regardless of go_service_health_host")
	}
}

func slicesContains(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

// kafka-run-class.sh's default KAFKA_JMX_OPTS starts an unauthenticated,
// plaintext JMX agent whose RMI ports listen on every interface; the broker,
// controller and MirrorMaker units start the JVM without it.
func TestKafkaUnitsDisableRemoteJMX(t *testing.T) {
	for _, unit := range []string{
		"ansible/collections/ansible_collections/frameworks/infra/roles/kafka/templates/kafka.service.j2",
		"ansible/collections/ansible_collections/frameworks/infra/roles/kafka_mirrormaker/templates/kafka-mirrormaker.service.j2",
	} {
		if content := readRepoFile(t, unit); !strings.Contains(content, `Environment="KAFKA_JMX_OPTS=-Djava.awt.headless=true"`) {
			t.Fatalf("%s leaves kafka-run-class.sh's remote JMX agent on:\n%s", unit, content)
		}
	}
}

// A stock mistserver.service from the host image runs every Mist connector on
// every interface; node_baseline stops and masks it on every host.
func TestNodeBaselineMasksStockMistServer(t *testing.T) {
	defaults := readRepoFile(t, "ansible/collections/ansible_collections/frameworks/infra/roles/node_baseline/defaults/main.yml")
	if !strings.Contains(defaults, "node_baseline_masked_stock_services:\n  - mistserver.service") {
		t.Fatalf("node_baseline does not list mistserver.service as a stock unit to mask:\n%s", defaults)
	}
	tasks := readRepoFile(t, "ansible/collections/ansible_collections/frameworks/infra/roles/node_baseline/tasks/stock_services.yml")
	for _, want := range []string{"state: stopped", "enabled: false", "masked: true", "loop: \"{{ node_baseline_masked_stock_services }}\""} {
		if !strings.Contains(tasks, want) {
			t.Fatalf("stock_services.yml lacks %q:\n%s", want, tasks)
		}
	}
	if main := readRepoFile(t, "ansible/collections/ansible_collections/frameworks/infra/roles/node_baseline/tasks/main.yml"); !strings.Contains(main, "import_tasks: stock_services.yml") {
		t.Fatal("node_baseline does not run stock_services.yml")
	}
}

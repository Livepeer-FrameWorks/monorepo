package provisioner

import (
	"context"
	"os/exec"
	"reflect"
	"strings"
	"testing"

	"frameworks/cli/pkg/inventory"

	"gopkg.in/yaml.v3"
)

const rolesDir = "ansible/collections/ansible_collections/frameworks/infra/roles/"

func exporterTestHelpers(t *testing.T, resolved map[string]bool) RoleBuildHelpers {
	t.Helper()
	return RoleBuildHelpers{
		DetectRemoteOS: func(context.Context, inventory.Host) (string, string, error) { return "linux", "amd64", nil },
		ResolveArtifact: func(name, arch, channel string, metadata map[string]any) (ResolvedArtifact, error) {
			resolved[name] = true
			return ResolvedArtifact{URL: "https://example.test/" + name + ".tar.gz", Checksum: "sha256:ab", Version: "vtest"}, nil
		},
	}
}

// Every vmagent host carries node_exporter and the host crash scan; without
// them disk latency, memory, systemd restarts and segfaults reach no rule.
func TestPrometheusStackVMAgentInstallsNodeExporter(t *testing.T) {
	resolved := map[string]bool{}
	vars, err := prometheusStackRoleVars(context.Background(), inventory.Host{}, ServiceConfig{
		Metadata: map[string]any{"component": "vmagent", "platform_channel": "stable"},
	}, exporterTestHelpers(t, resolved))
	if err != nil {
		t.Fatalf("prometheusStackRoleVars: %v", err)
	}
	if got := vars["prometheus_stack_components"]; !reflect.DeepEqual(got, []string{"node_exporter", "vmagent"}) {
		t.Fatalf("prometheus_stack_components = %#v, want node_exporter beside vmagent", got)
	}
	if !resolved[NodeExporterArtifact] || vars["node_exporter_artifact_url"] != "https://example.test/node-exporter.tar.gz" || vars["node_exporter_artifact_checksum"] != "sha256:ab" {
		t.Fatalf("node_exporter artifact vars = %v / %v", vars["node_exporter_artifact_url"], vars["node_exporter_artifact_checksum"])
	}
	if vars["node_exporter_port"] != NodeExporterPort {
		t.Fatalf("node_exporter_port = %v, want %d", vars["node_exporter_port"], NodeExporterPort)
	}

	role := rolesDir + "prometheus_stack/"
	main := readRepoFile(t, role+"tasks/main.yml")
	if !strings.Contains(main, "ansible.builtin.import_tasks: node_exporter.yml\n  tags: [install, configure, service]\n  when: \"'node_exporter' in prometheus_stack_components\"") {
		t.Fatalf("prometheus_stack main.yml does not install node_exporter for the component:\n%s", main)
	}
	unit := readRepoFile(t, role+"templates/node_exporter.service.j2")
	for _, want := range []string{
		"--web.listen-address={{ node_exporter_listen_address }}:{{ node_exporter_port }}",
		"--collector.systemd \\\n",
		"--collector.systemd.enable-restarts-metrics \\\n",
		"--collector.textfile.directory={{ node_exporter_textfile_dir }}",
	} {
		if !strings.Contains(unit, want) {
			t.Fatalf("node_exporter unit missing %q:\n%s", want, unit)
		}
	}
	defaults := readRepoFile(t, role+"defaults/main.yml")
	if !strings.Contains(defaults, "node_exporter_listen_address: 127.0.0.1\n") || !strings.Contains(defaults, "node_exporter_port: 9110\n") {
		t.Fatalf("node_exporter must default to loopback:9110:\n%s", defaults)
	}
	tasks := readRepoFile(t, role+"tasks/node_exporter.yml")
	if !strings.Contains(tasks, "name: frameworks-host-crash-scan.timer\n    state: started\n    enabled: true") {
		t.Fatalf("node_exporter tasks do not enable the host crash scan timer:\n%s", tasks)
	}
}

// Mist's MistProcLivepeer segfaulted on staging edges without failing any
// systemd unit; the crash scan is the record that reaches the alert rules.
func TestHostCrashScanCountsKernelAndCoredumpReports(t *testing.T) {
	awk, err := exec.LookPath("awk")
	if err != nil {
		t.Skip("awk not installed")
	}
	journal := strings.Join([]string{
		"MistProcLivepee[221429]: segfault at 64 ip 00005b67983e7b20 sp 00007ffe31529ed0 error 4 in MistProcLivepeer[5b6798210000+280000] likely on CPU 0 (core 0, socket 0)",
		"MistProcLivepee[229863]: segfault at 64 ip 000065338b4224d0 sp 00007ffd116c7720 error 4 in MistProcLivepeer[65338b24a000+281000] likely on CPU 1 (core 1, socket 0)",
		"traps: yb-tserver[4242] general protection fault ip:7f00 sp:7f00 error:0 in libc.so.6[7f00+1000]",
		"Process 4242 (yb-tserver) of user 1001 terminated abnormally with signal 11/SEGV, processing...",
		"Process 4242 (yb-tserver) of user 1001 dumped core.",
		"eth0: Link is Up - 1Gbps/Full",
	}, "\n") + "\n"
	cmd := exec.CommandContext(t.Context(), awk, "-f", "../../../"+rolesDir+"prometheus_stack/files/host-crash-scan.awk")
	cmd.Stdin = strings.NewReader(journal)
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("awk: %v", err)
	}
	got := string(out)
	for _, want := range []string{
		"# TYPE frameworks_host_process_crash_reports gauge\n",
		"frameworks_host_process_crash_reports{source=\"kernel\",process=\"MistProcLivepee\"} 2\n",
		"frameworks_host_process_crash_reports{source=\"kernel\",process=\"yb-tserver\"} 1\n",
		"frameworks_host_process_crash_reports{source=\"coredump\",process=\"yb-tserver\"} 1\n",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("crash scan output missing %q:\n%s", want, got)
		}
	}
	if strings.Count(got, "frameworks_host_process_crash_reports{") != 3 {
		t.Fatalf("crash scan counted non-crash lines:\n%s", got)
	}
}

// Brokers and controllers expose the KRaft quorum, replication and controller
// queue only over JMX; the javaagent serves them to the host-local vmagent.
func TestKafkaRoleLoadsJMXExporter(t *testing.T) {
	for role, port := range map[string]int{"broker": KafkaBrokerJMXPort, "controller": KafkaControllerJMXPort} {
		resolved := map[string]bool{}
		vars, err := kafkaRoleVarsFor(role)(context.Background(), inventory.Host{}, ServiceConfig{Metadata: map[string]any{}}, exporterTestHelpers(t, resolved))
		if err != nil {
			t.Fatalf("%s vars: %v", role, err)
		}
		if !resolved[jmxExporterArtifact] || vars["kafka_jmx_exporter_artifact_url"] != "https://example.test/jmx-prometheus-javaagent.tar.gz" {
			t.Fatalf("%s JMX exporter artifact not passed: %v", role, vars["kafka_jmx_exporter_artifact_url"])
		}
		if vars["kafka_jmx_port"] != port {
			t.Fatalf("%s kafka_jmx_port = %v, want %d", role, vars["kafka_jmx_port"], port)
		}
	}
	if KafkaBrokerJMXPort == KafkaControllerJMXPort || KafkaBrokerJMXPort == KafkaMirrorMakerJMXPort || KafkaControllerJMXPort == KafkaMirrorMakerJMXPort {
		t.Fatal("broker, controller and MirrorMaker2 share hosts and need distinct JMX ports")
	}
	unit := readRepoFile(t, rolesDir+"kafka/templates/kafka.service.j2")
	if !strings.Contains(unit, `Environment="KAFKA_OPTS=-javaagent:{{ kafka_jmx_exporter_jar }}=127.0.0.1:{{ kafka_jmx_port }}:{{ kafka_jmx_exporter_config_path }}"`) {
		t.Fatalf("kafka unit does not load the loopback JMX exporter:\n%s", unit)
	}
	config := readRepoFile(t, rolesDir+"kafka/templates/jmx-exporter.yml.j2")
	for _, want := range []string{"kafka.server<type=raft-metrics><>(current-leader|", "name=(UnderReplicatedPartitions|", "name=(ActiveControllerCount|OfflinePartitionsCount|", "name=(EventQueueTimeMs|EventQueueProcessingTimeMs)><>99thPercentile:"} {
		if !strings.Contains(config, want) {
			t.Fatalf("kafka JMX exporter config missing %q:\n%s", want, config)
		}
	}
}

func TestRedisRoleInstallsExporterForServersOnly(t *testing.T) {
	resolved := map[string]bool{}
	vars, err := redisRoleVars(context.Background(), inventory.Host{}, ServiceConfig{Port: 6380, Metadata: map[string]any{"instance": "chatwoot", "password": "pw"}}, exporterTestHelpers(t, resolved))
	if err != nil {
		t.Fatalf("redis vars: %v", err)
	}
	if !resolved[redisExporterArtifact] || vars["redis_exporter_artifact_url"] != "https://example.test/redis-exporter.tar.gz" {
		t.Fatalf("redis exporter artifact not passed: %v", vars["redis_exporter_artifact_url"])
	}
	if RedisExporterPort(6380) != 16380 {
		t.Fatalf("RedisExporterPort(6380) = %d, want 16380", RedisExporterPort(6380))
	}
	sentinel := map[string]bool{}
	sentinelVars, err := redisRoleVars(context.Background(), inventory.Host{}, ServiceConfig{Metadata: map[string]any{"instance": "foghorn", "redis_role": "sentinel"}}, exporterTestHelpers(t, sentinel))
	if err != nil {
		t.Fatalf("sentinel vars: %v", err)
	}
	if sentinel[redisExporterArtifact] || sentinelVars["redis_exporter_artifact_url"] != nil {
		t.Fatal("a Sentinel has no keyspace to export")
	}
	roleVars := readRepoFile(t, rolesDir+"redis/vars/main.yml")
	if !strings.Contains(roleVars, `redis_exporter_port: "{{ redis_port | int + 10000 }}"`) {
		t.Fatalf("redis role must derive the exporter port like RedisExporterPort:\n%s", roleVars)
	}
	unit := readRepoFile(t, rolesDir+"redis/templates/exporter.service.j2")
	if !strings.Contains(unit, "--web.listen-address=127.0.0.1:{{ redis_exporter_port }}") || strings.Contains(unit, "password") {
		t.Fatalf("redis exporter must listen on loopback and keep the password off argv:\n%s", unit)
	}
}

func TestPostgresRoleInstallsExporter(t *testing.T) {
	resolved := map[string]bool{}
	vars, err := postgresRoleVars(context.Background(), inventory.Host{}, ServiceConfig{Version: "18.4", Metadata: map[string]any{"postgres_password": "pw"}}, exporterTestHelpers(t, resolved))
	if err != nil {
		t.Fatalf("postgres vars: %v", err)
	}
	if !resolved[postgresExporterArtifact] || vars["postgres_exporter_artifact_url"] != "https://example.test/postgres-exporter.tar.gz" {
		t.Fatalf("postgres exporter artifact not passed: %v", vars["postgres_exporter_artifact_url"])
	}
	roleVars := readRepoFile(t, rolesDir+"postgres/vars/main.yml")
	if !strings.Contains(roleVars, `postgres_exporter_port: "{{ postgres_port | int + 10000 }}"`) || PostgresExporterPort(5432) != 15432 {
		t.Fatalf("postgres role must derive the exporter port like PostgresExporterPort:\n%s", roleVars)
	}
	unit := readRepoFile(t, rolesDir+"postgres/templates/exporter.service.j2")
	if !strings.Contains(unit, "User=postgres\n") || !strings.Contains(unit, "host=/var/run/postgresql") || !strings.Contains(unit, "--web.listen-address=127.0.0.1:{{ postgres_exporter_port }}") {
		t.Fatalf("postgres exporter must use peer auth over the socket and listen on loopback:\n%s", unit)
	}
}

// Native edges run the same host exporter and crash scan; the edge vmagent
// scrapes it beside Mist and Helmsman.
func TestNativeEdgeScrapesNodeExporter(t *testing.T) {
	tasks := readRepoFile(t, rolesDir+"edge/tasks/install-native-linux-vmagent.yml")
	if !strings.Contains(tasks, "name: frameworks.infra.prometheus_stack\n    tasks_from: node_exporter.yml") {
		t.Fatalf("native edge vmagent does not install node_exporter:\n%s", tasks)
	}
	scrape := readRepoFile(t, rolesDir+"edge/templates/vmagent-edge.yml.j2")
	if !strings.Contains(scrape, "  - job_name: edge-node-exporter\n") || !strings.Contains(scrape, `"127.0.0.1:{{ edge_node_exporter_port }}"`) {
		t.Fatalf("edge vmagent does not scrape node_exporter:\n%s", scrape)
	}
}

// A JVM exits 143 (128+SIGTERM) after a clean SIGTERM shutdown. Without
// SuccessExitStatus=143 every systemctl stop or restart of a Kafka broker,
// KRaft controller or MirrorMaker2 worker logs "Failed with result
// 'exit-code'", which doctor's host crash check counts as a crash.
func TestJVMUnitsTreatSIGTERMExitAsSuccess(t *testing.T) {
	for _, unit := range []string{
		"kafka/templates/kafka.service.j2",
		"kafka_mirrormaker/templates/kafka-mirrormaker.service.j2",
	} {
		t.Run(unit, func(t *testing.T) {
			text := readRepoFile(t, rolesDir+unit)
			section := ""
			found := false
			for _, line := range strings.Split(text, "\n") {
				line = strings.TrimSpace(line)
				if strings.HasPrefix(line, "[") && strings.HasSuffix(line, "]") {
					section = line
					continue
				}
				if section == "[Service]" && line == "SuccessExitStatus=143" {
					found = true
				}
			}
			if !found {
				t.Fatalf("%s [Service] lacks SuccessExitStatus=143:\n%s", unit, text)
			}
		})
	}
}

// Container edges share the host kernel, so they install the same host
// node_exporter and crash scan as native edges; the host-networked Linux
// vmagent container scrapes it on loopback. Docker Desktop on macOS has no
// host systemd, so a darwin container edge neither installs nor scrapes it.
func TestContainerEdgeInstallsAndScrapesNodeExporter(t *testing.T) {
	tasks := readRepoFile(t, rolesDir+"edge/tasks/install-container.yml")
	var parsed []map[string]any
	if err := yaml.Unmarshal([]byte(tasks), &parsed); err != nil {
		t.Fatal(err)
	}
	var install map[string]any
	for _, task := range parsed {
		if role, ok := task["ansible.builtin.include_role"].(map[string]any); ok &&
			role["name"] == "frameworks.infra.prometheus_stack" && role["tasks_from"] == "node_exporter.yml" {
			install = task
		}
	}
	if install == nil {
		t.Fatalf("container edge does not install node_exporter and the host crash scan:\n%s", tasks)
	}
	when, _ := install["when"].([]any)
	wantWhen := []any{"edge_telemetry_url | length > 0", "ansible_facts.os_family != 'Darwin'", "ansible_facts.service_mgr == 'systemd'"}
	if !reflect.DeepEqual(when, wantWhen) {
		t.Fatalf("container edge node_exporter install when = %#v, want %#v", when, wantWhen)
	}

	for _, tc := range []struct {
		name    string
		vars    map[string]any
		scraped bool
	}{
		{"native", map[string]any{"edge_mode": "native"}, true},
		{"container linux", map[string]any{"edge_mode": "container", "edge_container_flavor": "linux"}, true},
		{"container darwin", map[string]any{"edge_mode": "container", "edge_container_flavor": "darwin"}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			vars := map[string]any{"edge_node_id": "edge-1", "edge_node_exporter_port": NodeExporterPort}
			for k, v := range tc.vars {
				vars[k] = v
			}
			rendered := renderRoleTemplate(t, "edge/templates/vmagent-edge.yml.j2", vars)
			var cfg struct {
				ScrapeConfigs []struct {
					JobName       string `yaml:"job_name"`
					StaticConfigs []struct {
						Targets []string `yaml:"targets"`
					} `yaml:"static_configs"`
				} `yaml:"scrape_configs"`
			}
			if err := yaml.Unmarshal([]byte(rendered), &cfg); err != nil {
				t.Fatalf("rendered vmagent config is not YAML: %v\n%s", err, rendered)
			}
			var target string
			for _, sc := range cfg.ScrapeConfigs {
				if sc.JobName == "edge-node-exporter" {
					target = sc.StaticConfigs[0].Targets[0]
				}
			}
			if tc.scraped && target != "127.0.0.1:9110" {
				t.Fatalf("edge-node-exporter target = %q, want 127.0.0.1:9110:\n%s", target, rendered)
			}
			if !tc.scraped && target != "" {
				t.Fatalf("darwin container edge must not scrape node_exporter, got %q:\n%s", target, rendered)
			}
		})
	}
}

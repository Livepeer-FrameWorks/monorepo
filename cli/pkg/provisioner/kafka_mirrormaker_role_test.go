package provisioner

import (
	"context"
	"errors"
	"regexp"
	"strings"
	"testing"

	"frameworks/cli/pkg/inventory"
)

func TestKafkaMirrorMakerRoleVarsPinJMXExporter(t *testing.T) {
	helpers := RoleBuildHelpers{
		DetectRemoteOS: func(context.Context, inventory.Host) (string, string, error) {
			return "linux", "arm64", nil
		},
		ResolveArtifact: func(name, arch, channel string, metadata map[string]any) (ResolvedArtifact, error) {
			if arch != "linux-arm64" {
				t.Fatalf("ResolveArtifact(%s) arch = %q, want linux-arm64", name, arch)
			}
			switch name {
			case "kafka":
				return ResolvedArtifact{URL: "https://example.test/kafka.tgz", Checksum: "sha512:aa", Version: "4.2.0"}, nil
			case "jmx-prometheus-javaagent":
				return ResolvedArtifact{URL: "https://example.test/jmx_prometheus_javaagent-1.6.0.jar", Checksum: "sha256:bb", Version: "1.6.0"}, nil
			}
			return ResolvedArtifact{}, errors.New("unexpected artifact " + name)
		},
	}
	vars, err := kafkaMirrorMakerRoleVars(context.Background(), inventory.Host{}, ServiceConfig{
		Metadata: map[string]any{"platform_channel": "stable"},
	}, helpers)
	if err != nil {
		t.Fatalf("kafkaMirrorMakerRoleVars: %v", err)
	}
	if got := vars["kafka_mm_jmx_exporter_artifact_url"]; got != "https://example.test/jmx_prometheus_javaagent-1.6.0.jar" {
		t.Fatalf("kafka_mm_jmx_exporter_artifact_url = %v", got)
	}
	if got := vars["kafka_mm_jmx_exporter_artifact_checksum"]; got != "sha256:bb" {
		t.Fatalf("kafka_mm_jmx_exporter_artifact_checksum = %v", got)
	}
	if got := vars["kafka_mm_jmx_port"]; got != KafkaMirrorMakerJMXPort {
		t.Fatalf("kafka_mm_jmx_port = %v, want %d", got, KafkaMirrorMakerJMXPort)
	}
}

// A release manifest without the pinned javaagent must fail provisioning:
// an MM2 worker without its metrics endpoint would silently disable the
// replication-lag alert.
func TestKafkaMirrorMakerRoleVarsRequireJMXExporterArtifact(t *testing.T) {
	helpers := RoleBuildHelpers{
		DetectRemoteOS: func(context.Context, inventory.Host) (string, string, error) {
			return "linux", "amd64", nil
		},
		ResolveArtifact: func(name, arch, channel string, metadata map[string]any) (ResolvedArtifact, error) {
			if name == "kafka" {
				return ResolvedArtifact{URL: "https://example.test/kafka.tgz", Checksum: "sha512:aa", Version: "4.2.0"}, nil
			}
			return ResolvedArtifact{}, errors.New("release manifest has no entry named " + name)
		},
	}
	_, err := kafkaMirrorMakerRoleVars(context.Background(), inventory.Host{}, ServiceConfig{
		Metadata: map[string]any{"platform_channel": "stable"},
	}, helpers)
	if err == nil || !strings.Contains(err.Error(), "JMX exporter") {
		t.Fatalf("kafkaMirrorMakerRoleVars error = %v, want JMX exporter resolution failure", err)
	}
}

func TestKafkaMirrorMakerRoleLoadsLoopbackJMXExporter(t *testing.T) {
	unit := readRepoFile(t, "ansible/collections/ansible_collections/frameworks/infra/roles/kafka_mirrormaker/templates/kafka-mirrormaker.service.j2")
	want := `Environment="KAFKA_OPTS=-javaagent:{{ kafka_mm_jmx_exporter_jar }}=127.0.0.1:{{ kafka_mm_jmx_port }}:{{ kafka_mm_jmx_exporter_config_path }}"`
	if !strings.Contains(unit, want) {
		t.Fatalf("MirrorMaker unit must load the JMX exporter on loopback:\n%s", unit)
	}
	defaults := readRepoFile(t, "ansible/collections/ansible_collections/frameworks/infra/roles/kafka_mirrormaker/defaults/main.yml")
	if !strings.Contains(defaults, "kafka_mm_jmx_port: 9404") || KafkaMirrorMakerJMXPort != 9404 {
		t.Fatalf("role default JMX port and KafkaMirrorMakerJMXPort must both be 9404:\n%s", defaults)
	}
}

func TestKafkaMirrorMakerCheckModeHandlesMissingInstallDirectories(t *testing.T) {
	install := readRepoFile(t, "ansible/collections/ansible_collections/frameworks/infra/roles/kafka_mirrormaker/tasks/install.yml")
	for _, want := range []string{
		"register: kafka_mm_identity_probe",
		"owner: \"{{ kafka_mm_user if (not ansible_check_mode or (kafka_mm_identity_probe.rc | default(1)) == 0) else omit }}\"",
		"group: \"{{ kafka_mm_group if (not ansible_check_mode or (kafka_mm_identity_probe.rc | default(1)) == 0) else omit }}\"",
		"register: kafka_mm_jmx_exporter_stat",
		"kafka_mm_jmx_exporter_refresh_required",
	} {
		if !strings.Contains(install, want) {
			t.Errorf("MirrorMaker install tasks missing check-mode prerequisite %q", want)
		}
	}

	configure := readRepoFile(t, "ansible/collections/ansible_collections/frameworks/infra/roles/kafka_mirrormaker/tasks/configure.yml")
	for _, want := range []string{
		"register: kafka_mm_identity_probe",
		"register: kafka_mm_install_dir_stat",
		"register: kafka_mm_jmx_exporter_dir_stat",
		"not ansible_check_mode or (kafka_mm_install_dir_stat.stat.exists | default(false))",
		"not ansible_check_mode or (kafka_mm_jmx_exporter_dir_stat.stat.exists | default(false))",
	} {
		if !strings.Contains(configure, want) {
			t.Errorf("MirrorMaker configure tasks missing first-install guard %q", want)
		}
	}
}

func TestKafkaMirrorMakerArgumentSpecAcceptsRenderedSourceFields(t *testing.T) {
	meta := readRepoFile(t, "ansible/collections/ansible_collections/frameworks/infra/roles/kafka_mirrormaker/meta/main.yml")
	for _, field := range []string{"emit_checkpoints:", "tasks_max:"} {
		if !strings.Contains(meta, field) {
			t.Errorf("MirrorMaker argument spec rejects rendered source field %q", field)
		}
	}
}

// jmx_exporter patterns are unanchored. Without the ":" terminator the
// replication-latency-ms alternative also matches replication-latency-ms-max,
// collapsing max/avg into one metric name that the alert rules never see.
func TestKafkaMirrorMakerJMXExporterAttributesAreTerminated(t *testing.T) {
	config := readRepoFile(t, "ansible/collections/ansible_collections/frameworks/infra/roles/kafka_mirrormaker/templates/jmx-exporter.yml.j2")
	patterns := regexp.MustCompile(`(?m)^\s+- pattern: '(.*)'$`).FindAllStringSubmatch(config, -1)
	if len(patterns) == 0 {
		t.Fatalf("no exporter patterns found:\n%s", config)
	}
	for _, pattern := range patterns {
		if !strings.HasSuffix(pattern[1], "):") {
			t.Errorf("pattern must end its attribute group at ':': %s", pattern[1])
		}
	}
	for _, needle := range []string{
		"kafka.connect.mirror<type=MirrorSourceConnector, source=([^,]+), target=([^,]+), topic=([^,]+), partition=([0-9]+)>",
		"kafka.connect.mirror<type=MirrorCheckpointConnector, source=([^,]+), target=([^,]+), group=([^,]+), topic=([^,]+), partition=([0-9]+)>",
		"replication-latency-ms-max",
	} {
		if !strings.Contains(config, needle) {
			t.Errorf("exporter config missing %q", needle)
		}
	}
}

// A host that is no longer a link worker has no link or artifact inputs, so
// cleanup renders no install vars and runs the dedicated cleanup playbook
// instead of the role entry point that validates them.
func TestKafkaMirrorMakerCleanupOnlyNeedsNoInstallInputs(t *testing.T) {
	config := ServiceConfig{Mode: "native", Metadata: map[string]any{"_cleanup_only": true}}
	vars, err := kafkaMirrorMakerRoleVars(context.Background(), inventory.Host{Name: "regional-us-1"}, config, RoleBuildHelpers{})
	if err != nil {
		t.Fatalf("cleanup-only vars: %v", err)
	}
	if len(vars) != 0 {
		t.Fatalf("cleanup-only vars = %#v, want none", vars)
	}
	if got := kafkaMirrorMakerPlaybookSelector(config); got != KafkaMirrorMakerCleanupPlaybook {
		t.Fatalf("cleanup playbook = %q, want %q", got, KafkaMirrorMakerCleanupPlaybook)
	}
	if got := kafkaMirrorMakerPlaybookSelector(ServiceConfig{Mode: "native"}); got != "playbooks/kafka_mirrormaker.yml" {
		t.Fatalf("provision playbook = %q", got)
	}
}

// MirrorMaker2 shares /opt/kafka with brokers and controllers on the same
// host, so worker cleanup removes only the worker's unit, config, and exporter.
func TestKafkaMirrorMakerCleanupKeepsSharedKafkaInstall(t *testing.T) {
	playbook := readRepoFile(t, "ansible/"+KafkaMirrorMakerCleanupPlaybook)
	for _, want := range []string{"frameworks.infra.kafka_mirrormaker", "tasks_from: cleanup", "tags: [cleanup]"} {
		if !strings.Contains(playbook, want) {
			t.Errorf("cleanup playbook missing %q", want)
		}
	}
	tasks := readRepoFile(t, "ansible/collections/ansible_collections/frameworks/infra/roles/kafka_mirrormaker/tasks/cleanup.yml")
	for _, want := range []string{"name: frameworks-kafka-mirrormaker", "state: stopped", "/etc/systemd/system/frameworks-kafka-mirrormaker.service", "kafka_mm_properties_path", "kafka_mm_jmx_exporter_dir"} {
		if !strings.Contains(tasks, want) {
			t.Errorf("cleanup tasks missing %q", want)
		}
	}
	for _, shared := range []string{"kafka_mm_install_dir", "kafka_mm_state_dir", "frameworks-kafka.service", "frameworks-kafka-controller"} {
		if strings.Contains(tasks, shared) {
			t.Errorf("cleanup tasks touch shared Kafka state %q", shared)
		}
	}
	main := readRepoFile(t, "ansible/collections/ansible_collections/frameworks/infra/roles/kafka_mirrormaker/tasks/main.yml")
	if !strings.Contains(main, "import_tasks: cleanup.yml") || !strings.Contains(main, "tags: [cleanup, never]") {
		t.Error("role main.yml must import cleanup.yml under the cleanup tag only")
	}
}

func mirrorMakerTestHelpers(t *testing.T) RoleBuildHelpers {
	t.Helper()
	return RoleBuildHelpers{
		DetectRemoteOS: func(context.Context, inventory.Host) (string, string, error) {
			return "linux", "amd64", nil
		},
		ResolveArtifact: func(name, _, _ string, _ map[string]any) (ResolvedArtifact, error) {
			return ResolvedArtifact{URL: "https://example.test/" + name, Checksum: "sha256:aa", Version: "4.2.0"}, nil
		},
	}
}

// Dedicated-mode followers forward task configs to the leader over internode
// REST; that endpoint is unauthenticated, so it binds and advertises the mesh
// IP and never a public interface.
func TestKafkaMirrorMakerRESTBindsMeshAddress(t *testing.T) {
	config := ServiceConfig{Metadata: map[string]any{"platform_channel": "stable"}}
	vars, err := kafkaMirrorMakerRoleVars(context.Background(), inventory.Host{Name: "regional-eu-1", ExternalIP: "203.0.113.7", WireguardIP: "10.88.1.11"}, config, mirrorMakerTestHelpers(t))
	if err != nil {
		t.Fatal(err)
	}
	if got := vars["kafka_mm_rest_address"]; got != "10.88.1.11" {
		t.Fatalf("kafka_mm_rest_address = %v, want the mesh IP", got)
	}
	if _, ok := vars["kafka_mm_defer_restart"]; ok {
		t.Fatal("kafka_mm_defer_restart set without the metadata flag")
	}

	vars, err = kafkaMirrorMakerRoleVars(context.Background(), inventory.Host{Name: "solo", ExternalIP: "203.0.113.8"}, config, mirrorMakerTestHelpers(t))
	if err != nil {
		t.Fatal(err)
	}
	if got := vars["kafka_mm_rest_address"]; got != "127.0.0.1" {
		t.Fatalf("kafka_mm_rest_address without a mesh IP = %v, want loopback", got)
	}
}

func TestKafkaMirrorMakerDeferRestartFlagReachesRole(t *testing.T) {
	config := ServiceConfig{Metadata: map[string]any{"platform_channel": "stable", KafkaMirrorMakerDeferRestartKey: true}}
	vars, err := kafkaMirrorMakerRoleVars(context.Background(), inventory.Host{WireguardIP: "10.88.1.11"}, config, mirrorMakerTestHelpers(t))
	if err != nil {
		t.Fatal(err)
	}
	if vars["kafka_mm_defer_restart"] != true {
		t.Fatalf("kafka_mm_defer_restart = %v, want true", vars["kafka_mm_defer_restart"])
	}
	defaults := readRepoFile(t, "ansible/collections/ansible_collections/frameworks/infra/roles/kafka_mirrormaker/defaults/main.yml")
	if !strings.Contains(defaults, "kafka_mm_restart_pending_path: "+KafkaMirrorMakerRestartPendingPath+"\n") {
		t.Fatalf("role default kafka_mm_restart_pending_path must equal KafkaMirrorMakerRestartPendingPath %q", KafkaMirrorMakerRestartPendingPath)
	}
	handlers := readRepoFile(t, "ansible/collections/ansible_collections/frameworks/infra/roles/kafka_mirrormaker/handlers/main.yml")
	for _, want := range []string{
		"- not (kafka_mm_defer_restart | bool)",
		"path: \"{{ kafka_mm_restart_pending_path }}\"\n    state: touch",
		"when: kafka_mm_defer_restart | bool",
	} {
		if !strings.Contains(handlers, want) {
			t.Errorf("handlers missing %q:\n%s", want, handlers)
		}
	}
	restart := readRepoFile(t, "ansible/collections/ansible_collections/frameworks/infra/roles/kafka_mirrormaker/tasks/restart.yml")
	if !strings.Contains(restart, "state: restarted") || !strings.Contains(restart, "path: \"{{ kafka_mm_restart_pending_path }}\"\n    state: absent") {
		t.Fatalf("restart tag must restart the worker and clear the pending marker:\n%s", restart)
	}
}

func TestKafkaMirrorMakerPropertiesEnableInternodeREST(t *testing.T) {
	props := readRepoFile(t, "ansible/collections/ansible_collections/frameworks/infra/roles/kafka_mirrormaker/templates/mm2.properties.j2")
	for _, want := range []string{
		"dedicated.mode.enable.internode.rest = true\n",
		"listeners = http://{{ kafka_mm_rest_address }}:{{ kafka_mm_rest_port }}\n",
		"rest.advertised.host.name = {{ kafka_mm_rest_address }}\n",
		"rest.advertised.port = {{ kafka_mm_rest_port }}\n",
		"refresh.topics.interval.seconds = 60\n",
		"refresh.groups.interval.seconds = 60\n",
	} {
		if !strings.Contains(props, want) {
			t.Errorf("mm2.properties.j2 missing %q", want)
		}
	}
	if strings.Contains(props, "rest.port =") {
		t.Error("mm2.properties.j2 still sets rest.port, which listeners supersedes")
	}
}

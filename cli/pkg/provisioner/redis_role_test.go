package provisioner

import (
	"os"
	"strings"
	"testing"
)

// Sentinel persists role changes with CONFIG REWRITE into the file the process
// was started with, so that file must live in the runtime-owned data dir, and
// provisioning must never template a replication role into it.
func TestRedisRuntimeConfigIsSentinelOwned(t *testing.T) {
	role := "ansible/collections/ansible_collections/frameworks/infra/roles/redis/"
	vars := readRedisRepoFile(t, role+"vars/main.yml")
	install := readRedisRepoFile(t, role+"tasks/install.yml")
	unit := readRedisRepoFile(t, role+"templates/instance.service.j2")
	declared := readRedisRepoFile(t, role+"templates/instance.conf.j2")
	runtime := readRedisRepoFile(t, role+"templates/runtime.conf.j2")

	for _, want := range []string{
		"redis_data_dir ~ '/sentinel.conf' if redis_role == 'sentinel' else",
		"redis_data_dir ~ '/runtime.conf'",
	} {
		if !strings.Contains(vars, want) {
			t.Fatalf("runtime config must live in the data dir; vars missing %q", want)
		}
	}
	if strings.Count(unit, "{{ redis_runtime_config_path }}") != 2 || strings.Contains(unit, "{{ redis_config_path }}") {
		t.Fatalf("named units must start from the runtime config:\n%s", unit)
	}
	if strings.Contains(declared, "\nreplicaof ") {
		t.Fatalf("declared config must not carry a replication role:\n%s", declared)
	}
	if !strings.Contains(runtime, "include {{ redis_config_path }}") {
		t.Fatalf("runtime config must include the declared config:\n%s", runtime)
	}
	for _, want := range []string{
		"redis_named_config.changed or not redis_runtime_config_stat.stat.exists",
		`owner: "{{ omit if ansible_check_mode else redis_runtime_user }}"`,
		`test -w "{{ redis_runtime_config_path }}"`,
		"SENTINEL get-master-addr-by-name",
		"INFO replication",
	} {
		if !strings.Contains(install, want) {
			t.Fatalf("install must keep the live role on re-runs; missing %q", want)
		}
	}
}

// Any server can be demoted by Sentinel, and a demoted primary without
// masterauth cannot sync from the new primary (NOAUTH).
func TestRedisEveryServerCarriesMasterAuth(t *testing.T) {
	declared := readRedisRepoFile(t, "ansible/collections/ansible_collections/frameworks/infra/roles/redis/templates/instance.conf.j2")
	if strings.Contains(declared, "redis_role == 'replica'") || !strings.Contains(declared, "masterauth {{ redis_password }}") {
		t.Fatalf("masterauth must not depend on the declared role:\n%s", declared)
	}
}

func TestRedisStartupDiagnosticsIncludeRedisLog(t *testing.T) {
	install := readRedisRepoFile(t, "ansible/collections/ansible_collections/frameworks/infra/roles/redis/tasks/install.yml")
	for _, want := range []string{
		"Capture named Redis log after failed start",
		`src: "{{ redis_log_file }}"`,
		"redis log:",
	} {
		if !strings.Contains(install, want) {
			t.Fatalf("redis startup diagnostics should include Redis' own log output; missing %q", want)
		}
	}
}

func TestRedisSystemdServiceNameIsInstanceScoped(t *testing.T) {
	tests := []struct {
		name string
		cfg  ServiceConfig
		want string
	}{
		{
			name: "unnamed default instance falls back to legacy detector",
			cfg:  ServiceConfig{Metadata: map[string]any{}},
			want: "",
		},
		{
			name: "replica uses exact named service",
			cfg:  ServiceConfig{Metadata: map[string]any{"instance": "foghorn-media-us-1-replica-regional-us-2", "redis_role": "replica"}},
			want: "frameworks-redis-foghorn-media-us-1-replica-regional-us-2",
		},
		{
			name: "sentinel uses sentinel unit suffix",
			cfg:  ServiceConfig{Metadata: map[string]any{"instance_name": "foghorn-media-us-1", "redis_role": "sentinel"}},
			want: "frameworks-redis-foghorn-media-us-1-sentinel",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := redisSystemdServiceName(tt.cfg); got != tt.want {
				t.Fatalf("redisSystemdServiceName() = %q, want %q", got, tt.want)
			}
		})
	}
}

func readRedisRepoFile(t *testing.T, path string) string {
	t.Helper()
	content, err := os.ReadFile("../../../" + path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(content)
}

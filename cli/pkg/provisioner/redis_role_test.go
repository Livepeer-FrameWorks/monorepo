package provisioner

import (
	"strings"
	"testing"
)

func TestRedisSentinelConfigIsRuntimeWritable(t *testing.T) {
	vars := readRepoFile(t, "ansible/collections/ansible_collections/frameworks/infra/roles/redis/vars/main.yml")
	install := readRepoFile(t, "ansible/collections/ansible_collections/frameworks/infra/roles/redis/tasks/install.yml")

	for _, want := range []string{
		"redis_data_dir ~ '/sentinel.conf' if redis_role == 'sentinel'",
		`owner: "{{ redis_runtime_user if redis_role == 'sentinel' else 'root' }}"`,
		`test -w "{{ redis_config_path }}"`,
	} {
		if !strings.Contains(vars+install, want) {
			t.Fatalf("redis sentinel config must be writable by the runtime user; missing %q", want)
		}
	}
}

func TestRedisStartupDiagnosticsIncludeRedisLog(t *testing.T) {
	install := readRepoFile(t, "ansible/collections/ansible_collections/frameworks/infra/roles/redis/tasks/install.yml")
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

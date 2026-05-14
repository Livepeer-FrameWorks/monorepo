package provisioner

import (
	"os"
	"strings"
	"testing"
)

func TestRedisSentinelConfigIsRuntimeWritable(t *testing.T) {
	vars := readRedisRepoFile(t, "ansible/collections/ansible_collections/frameworks/infra/roles/redis/vars/main.yml")
	install := readRedisRepoFile(t, "ansible/collections/ansible_collections/frameworks/infra/roles/redis/tasks/install.yml")

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

func readRedisRepoFile(t *testing.T, path string) string {
	t.Helper()
	content, err := os.ReadFile("../../../" + path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(content)
}

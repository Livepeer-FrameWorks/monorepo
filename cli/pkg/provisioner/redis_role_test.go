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

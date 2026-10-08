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

// A replica that cannot authenticate logs a line every few milliseconds; a
// log file has no size cap and fills the root disk, journald does.
func TestRedisNamedInstancesLogToJournald(t *testing.T) {
	role := "ansible/collections/ansible_collections/frameworks/infra/roles/redis/"
	for _, template := range []string{"templates/instance.conf.j2", "templates/sentinel.conf.j2"} {
		content := readRedisRepoFile(t, role+template)
		if !strings.Contains(content, "\nlogfile \"\"\n") {
			t.Fatalf("%s must log to stdout (journald), got:\n%s", template, content)
		}
	}
	install := readRedisRepoFile(t, role+"tasks/install.yml")
	for _, want := range []string{
		"Capture named Redis journal after failed start",
		"Remove unrotated named Redis log file",
		`path: "{{ redis_named_log_file }}"`,
		"Remove empty named Redis log directory",
	} {
		if !strings.Contains(install, want) {
			t.Fatalf("install must keep startup diagnostics in the journal and remove file logs; missing %q", want)
		}
	}
	if strings.Contains(install, "redis_log_dir") || strings.Contains(install, "redis_log_file") {
		t.Fatal("install must not create or write a named Redis log file")
	}
}

// A restarted server listens while it loads its dataset or resyncs and answers
// PING with LOADING until then, so validation must wait for PONG instead of
// failing on the first reply, and must show the reply when it gives up. The
// password therefore travels in REDISCLI_AUTH, not argv, so the result needs
// no no_log.
func TestRedisValidatePingWaitsForPongAndShowsTheReply(t *testing.T) {
	validate := readRedisRepoFile(t, "ansible/collections/ansible_collections/frameworks/infra/roles/redis/tasks/validate.yml")
	start := strings.Index(validate, "- name: PING via redis-cli")
	if start < 0 {
		t.Fatal("validate.yml has no redis-cli PING task")
	}
	task := validate[start:]
	if next := strings.Index(task[1:], "\n- name:"); next >= 0 {
		task = task[:next+1]
	}
	for _, want := range []string{"until: \"'PONG' in redis_ping.stdout\"", "retries:", "delay:", "REDISCLI_AUTH"} {
		if !strings.Contains(task, want) {
			t.Fatalf("PING task missing %q:\n%s", want, task)
		}
	}
	for _, forbidden := range []string{"no_log", "'-a'", "redis_password]"} {
		if strings.Contains(task, forbidden) {
			t.Fatalf("PING task must not contain %q:\n%s", forbidden, task)
		}
	}
}

// Release convergence probes live roles over SSH; the password must be read on
// the host, never passed on the command line.
func TestRedisCLICommandTargetsTheInstanceWithoutThePassword(t *testing.T) {
	server := RedisCLICommand(ServiceConfig{Port: 6380, Metadata: map[string]any{
		"instance": "foghorn", "redis_role": "replica", "bind": "10.88.0.2 127.0.0.1", "password": "s3cret",
	}}, "INFO", "replication")
	for _, want := range []string{"/etc/frameworks/redis/foghorn.conf", "-h '\\''127.0.0.1'\\'' -p 6380 '\\''INFO'\\'' '\\''replication'\\''", "REDISCLI_AUTH", "sudo -n sh -c"} {
		if !strings.Contains(server, want) {
			t.Fatalf("server command missing %q:\n%s", want, server)
		}
	}
	sentinel := RedisCLICommand(ServiceConfig{Port: 6380, Metadata: map[string]any{
		"instance": "foghorn", "redis_role": "sentinel", "redis_sentinel_port": 26390, "bind": "10.88.0.2", "password": "s3cret",
	}}, "SENTINEL", "CKQUORUM", "foghorn")
	for _, want := range []string{"/var/lib/frameworks/redis/foghorn-sentinel/sentinel.conf", "-h '\\''10.88.0.2'\\'' -p 26390"} {
		if !strings.Contains(sentinel, want) {
			t.Fatalf("sentinel command missing %q:\n%s", want, sentinel)
		}
	}
	for _, command := range []string{server, sentinel} {
		if strings.Contains(command, "s3cret") {
			t.Fatalf("command carries the password:\n%s", command)
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

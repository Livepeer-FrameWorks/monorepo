package provisioner

import (
	"bufio"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

const testRolesDir = "ansible/collections/ansible_collections/frameworks/infra/roles/"

// skipUnlessCI skips the test on a host without an optional tool dependency.
// CI installs every one of them before the Go tests run, so there a missing
// dependency fails the test instead of hiding it behind a skip.
func skipUnlessCI(t *testing.T, format string, args ...any) {
	t.Helper()
	if strings.TrimSpace(os.Getenv("CI")) != "" {
		t.Fatalf(format, args...)
	}
	t.Skipf(format, args...)
}

func requireAnsiblePlaybook(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("ansible-playbook"); err != nil {
		skipUnlessCI(t, "ansible-playbook not available")
	}
}

// requirePinnedCollections requires every collection ansible/requirements.yml
// pins to be installed in the galaxy cache under the repo's ansible dir.
func requirePinnedCollections(t *testing.T, ansibleDir string) {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(ansibleDir, "requirements.yml"))
	if err != nil {
		t.Fatal(err)
	}
	var requirements struct {
		Collections []struct {
			Name string `yaml:"name"`
		} `yaml:"collections"`
	}
	if err = yaml.Unmarshal(raw, &requirements); err != nil {
		t.Fatalf("parse requirements.yml: %v", err)
	}
	var missing []string
	for _, collection := range requirements.Collections {
		namespace, name, _ := strings.Cut(collection.Name, ".")
		if _, statErr := os.Stat(filepath.Join(ansibleDir, ".cache", "collections", "ansible_collections", namespace, name)); statErr != nil {
			missing = append(missing, collection.Name)
		}
	}
	if len(missing) > 0 {
		skipUnlessCI(t, "collections %s are not installed under ansible/.cache/collections; run make ansible-galaxy-install", strings.Join(missing, ", "))
	}
}

func repoAnsibleDir(t *testing.T) string {
	t.Helper()
	dir, err := filepath.Abs("../../../ansible")
	if err != nil {
		t.Fatal(err)
	}
	return dir
}

// renderRoleTemplate renders one role template through ansible's template
// module, the path the role itself takes.
func renderRoleTemplate(t *testing.T, templateRel string, vars map[string]any) string {
	t.Helper()
	requireAnsiblePlaybook(t)
	src, err := filepath.Abs("../../../" + testRolesDir + templateRel)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	out := filepath.Join(dir, "rendered")
	playbook := filepath.Join(dir, "render.yml")
	if err = os.WriteFile(playbook, []byte(`- hosts: localhost
  gather_facts: false
  tasks:
    - ansible.builtin.template:
        src: "`+src+`"
        dest: "`+out+`"
        mode: "0600"
`), 0o600); err != nil {
		t.Fatal(err)
	}
	varsPath := filepath.Join(dir, "vars.json")
	encoded, err := json.Marshal(vars)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(varsPath, encoded, 0o600); err != nil {
		t.Fatal(err)
	}
	cmd := exec.CommandContext(t.Context(), "ansible-playbook", "-i", "localhost,", "-c", "local", playbook, "-e", "@"+varsPath)
	if output, runErr := cmd.CombinedOutput(); runErr != nil {
		t.Fatalf("ansible-playbook: %v\n%s", runErr, output)
	}
	rendered, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	return string(rendered)
}

// playbookTasksUnderTags lists the task names a playbook runs with the given
// tags, as ansible-playbook --list-tasks resolves its static imports.
func playbookTasksUnderTags(t *testing.T, playbookRel string, tags []string, extraVars map[string]any) []string {
	t.Helper()
	requireAnsiblePlaybook(t)
	root := repoAnsibleDir(t)
	requirePinnedCollections(t, root)
	encoded, err := json.Marshal(extraVars)
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.CommandContext(t.Context(), "ansible-playbook", "-i", "localhost,", "--list-tasks",
		"--tags", strings.Join(tags, ","), "-e", string(encoded), filepath.Join(root, playbookRel))
	cmd.Dir = root
	// Same search path as ansible.cfg: the source collection, then the galaxy
	// cache that holds its dependencies (ansible.posix and the community ones).
	// Collections bundled with the ansible install are not searched, so the
	// listing resolves the pinned versions or fails.
	cmd.Env = append(os.Environ(),
		"ANSIBLE_COLLECTIONS_PATH="+filepath.Join(root, "collections")+":"+filepath.Join(root, ".cache", "collections"),
		"ANSIBLE_COLLECTIONS_SCAN_SYS_PATH=False")
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("ansible-playbook --list-tasks %s: %v\n%s", playbookRel, err, output)
	}
	var names []string
	scanner := bufio.NewScanner(strings.NewReader(string(output)))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		name, _, ok := strings.Cut(line, "\tTAGS:")
		if !ok {
			continue
		}
		// Role tasks are listed as "<namespace.collection.role> : <task name>".
		if _, task, inRole := strings.Cut(name, " : "); inRole {
			name = task
		}
		names = append(names, strings.TrimSpace(name))
	}
	return names
}

// provisionTags are the tags RolePlaybookProvisioner.Provision runs; its
// check-mode precheck runs the same set without validate.
var provisionTags = []string{"install", "configure", "service", "validate"}

// Cluster provision and release apply converge node_tuning through
// RolePlaybookProvisioner, which selects the install/configure/service/validate
// tags. The role's policy imports carried only their own tags (apt,
// needrestart, timers, ...), so on core and regional hosts the precheck saw no
// change and the apt timers, unattended-upgrades and needrestart auto-restart
// stayed live: a libsystemd upgrade at 06:18 restarted Valkey and Postgres.
func TestNodeTuningPolicyRunsUnderProvisionTags(t *testing.T) {
	tasks := playbookTasksUnderTags(t, "playbooks/node_tuning.yml", provisionTags, nil)
	for _, want := range []string{
		"Node_tuning | write apt policy drop-in",
		"Node_tuning | write needrestart drop-in",
		"Node_tuning | mask background units",
		"Node_tuning | write core sysctl baseline",
		"Node_tuning | write limits baseline",
		"Node_tuning verify | assert apt timers masked",
	} {
		if !slices.Contains(tasks, want) {
			t.Errorf("provision tags %v do not run %q; tasks run: %q", provisionTags, want, tasks)
		}
	}
}

// Provision now runs node_tuning's verify tasks, and on Yugabyte hosts the
// yugabyte role's 99-yugabyte.conf sets vm.swappiness=0 over the baseline's 10.
// The runtime assertion skips every key a drop-in sorting after the baseline
// (or /etc/sysctl.conf) sets, so it does not fail those hosts, and still checks
// the keys only the baseline sets.
func TestNodeTuningVerifyLeavesKeysOfLaterDropInsToTheirRole(t *testing.T) {
	var tasks []map[string]any
	if err := yaml.Unmarshal([]byte(readRepoFile(t, testRolesDir+"node_tuning/tasks/verify.yml")), &tasks); err != nil {
		t.Fatal(err)
	}
	var script string
	var assertWhen []any
	for _, task := range tasks {
		switch task["name"] {
		case "Node_tuning verify | sysctl keys later drop-ins set # noqa: risky-shell-pipe", "Node_tuning verify | sysctl keys later drop-ins set":
			script, _ = task["ansible.builtin.shell"].(string)
		case "Node_tuning verify | assert sysctl runtime values":
			assertWhen, _ = task["when"].([]any)
		}
	}
	if script == "" {
		t.Fatal("node_tuning verify has no task listing the sysctl keys later drop-ins set")
	}
	if !slices.Contains(assertWhen, any("item.item.key not in node_tuning_sysctl_overridden.stdout_lines")) {
		t.Fatalf("runtime sysctl assertion does not skip keys later drop-ins set: when = %v", assertWhen)
	}

	root := t.TempDir()
	files := map[string]string{
		"etc/sysctl.d/10-early.conf":               "kernel.pid_max = 1\n",
		"etc/sysctl.d/90-frameworks-baseline.conf": "vm.swappiness = 10\nnet.core.somaxconn = 16384\n",
		"etc/sysctl.d/91-frameworks-edge.conf":     "net.core.default_qdisc = fq\n",
		"etc/sysctl.d/92-frameworks-redis.conf":    "vm.overcommit_memory=1\n",
		"etc/sysctl.d/99-yugabyte.conf":            "# yugabyte\nvm.swappiness=0\nvm.max_map_count=262144\n",
		"etc/sysctl.conf":                          "  -net.ipv4.ip_local_port_range = 1024 65000\n",
	}
	for rel, content := range files {
		path := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	output, err := exec.CommandContext(t.Context(), "sh", "-c", strings.ReplaceAll(script, "/etc/", root+"/etc/")).CombinedOutput()
	if err != nil {
		t.Fatalf("sh: %v\n%s", err, output)
	}
	got := strings.Fields(string(output))
	want := []string{"net.ipv4.ip_local_port_range", "vm.max_map_count", "vm.overcommit_memory", "vm.swappiness"}
	if !slices.Equal(got, want) {
		t.Fatalf("keys owned by later drop-ins = %q, want %q", got, want)
	}
}

// evalNeedrestartOverrides loads conf the way needrestart does (Perl `do`
// after the packaged defaults) and reports, per unit, the override_rc value
// of the first matching pattern: 0 keeps the unit from being restarted, -1
// means no pattern matched.
func evalNeedrestartOverrides(t *testing.T, conf string, units []string) (restartMode string, overrides map[string]int) {
	t.Helper()
	if _, err := exec.LookPath("perl"); err != nil {
		t.Skip("perl not available")
	}
	path := filepath.Join(t.TempDir(), "zz-frameworks.conf")
	if err := os.WriteFile(path, []byte(conf), 0o600); err != nil {
		t.Fatal(err)
	}
	script := `our %nrconf = (restart => 'i', override_rc => { qr(^bluetooth) => 0 });
my $file = shift @ARGV;
do $file or die "load $file: $@ $!";
print "restart=$nrconf{restart}\n";
for my $unit (@ARGV) {
	my $value = -1;
	for my $re (keys %{$nrconf{override_rc}}) {
		next unless $unit =~ /$re/;
		$value = $nrconf{override_rc}->{$re};
		last;
	}
	print "$unit=$value\n";
}`
	args := append([]string{"-e", script, path}, units...)
	output, err := exec.CommandContext(t.Context(), "perl", args...).CombinedOutput()
	if err != nil {
		t.Fatalf("perl: %v\n%s", err, output)
	}
	overrides = map[string]int{}
	for line := range strings.SplitSeq(strings.TrimSpace(string(output)), "\n") {
		key, value, _ := strings.Cut(line, "=")
		if key == "restart" {
			restartMode = value
			continue
		}
		switch value {
		case "0":
			overrides[key] = 0
		case "1":
			overrides[key] = 1
		default:
			overrides[key] = -1
		}
	}
	return restartMode, overrides
}

// needrestart restarts every service linked against an upgraded library unless
// an override_rc pattern maps it to 0. NEEDRESTART_MODE=a in the environment
// overrides the list-only mode, so data and platform services must be pinned
// by override_rc too.
func TestNodeTuningNeedrestartNeverRestartsDataServices(t *testing.T) {
	conf := renderRoleTemplate(t, "node_tuning/templates/needrestart_zz-frameworks.conf.j2", map[string]any{})
	dataUnits := []string{
		"valkey-server.service",
		"redis-server.service",
		"frameworks-redis-foghorn.service",
		"frameworks-redis-foghorn-sentinel.service",
		"frameworks-redis-chatwoot.service",
		"postgresql@18-main.service",
		"postgresql.service",
		"yb-master.service",
		"yb-tserver.service",
		"clickhouse-server.service",
		"clickhouse-keeper.service",
		"frameworks-kafka.service",
		"frameworks-kafka-controller.service",
		"frameworks-kafka-mirrormaker.service",
		"mistserver.service",
		"frameworks-mistserver.service",
		"frameworks-foghorn.service",
		"frameworks-helmsman.service",
		"frameworks-caddy.service",
		"docker.service",
		"containerd.service",
		"victoriametrics.service",
		"vmagent.service",
		"frameworks-vmagent-edge.service",
	}
	ordinary := []string{"ssh.service", "cron.service", "systemd-journald.service", "fail2ban.service"}
	mode, overrides := evalNeedrestartOverrides(t, conf, append(slices.Clone(dataUnits), ordinary...))
	if mode != "l" {
		t.Errorf("needrestart restart mode = %q, want list-only %q", mode, "l")
	}
	for _, unit := range dataUnits {
		if overrides[unit] != 0 {
			t.Errorf("needrestart may restart %s (override_rc match = %d, want 0)", unit, overrides[unit])
		}
	}
	for _, unit := range ordinary {
		if overrides[unit] == 0 {
			t.Errorf("needrestart override_rc pins %s, which is not a platform service", unit)
		}
	}
}

// Valkey warned "Memory overcommit must be enabled!" at every start on every
// Redis host: a background save or full resync forks, and under heuristic
// overcommit that fork can fail. The redis role sets vm.overcommit_memory=1 on
// every host it installs a server on, under the tags provisioning runs.
func TestRedisRoleEnablesMemoryOvercommitUnderProvisionTags(t *testing.T) {
	tasks := playbookTasksUnderTags(t, "playbooks/redis.yml", provisionTags, nil)
	const task = "Redis | allow memory overcommit for background saves and resyncs"
	if !slices.Contains(tasks, task) {
		t.Fatalf("provision tags %v do not run %q; tasks run: %q", provisionTags, task, tasks)
	}
	sysctl := readRepoFile(t, testRolesDir+"redis/tasks/sysctl.yml")
	for _, want := range []string{"name: vm.overcommit_memory", `value: "1"`, "sysctl_set: true", "sysctl_file: /etc/sysctl.d/92-frameworks-redis.conf"} {
		if !strings.Contains(sysctl, want) {
			t.Fatalf("redis sysctl task missing %q:\n%s", want, sysctl)
		}
	}
}

// MirrorMaker2's periodic topic config sync on Kafka 4.0-4.2 logs "Could not
// alter configuration of topic" for every mirrored copy after each sync, and
// the kafka role already declares each copy with its platform config, so every
// flow turns the sync off.
func TestKafkaMirrorMakerPropertiesDisableTopicConfigSync(t *testing.T) {
	props := renderRoleTemplate(t, "kafka_mirrormaker/templates/mm2.properties.j2", map[string]any{
		"kafka_mm_rest_address":    "10.88.0.11",
		"kafka_mm_rest_port":       8083,
		"kafka_mm_task_count":      2,
		"kafka_mm_exclude_pattern": "",
		"kafka_mm_target": map[string]any{
			"alias": "eu-west", "bootstrap_servers": "10.88.0.11:9092", "replicas": 3,
		},
		"kafka_mm_sources": []map[string]any{
			{"alias": "us-east", "bootstrap_servers": "10.88.1.11:9092", "topics": "analytics_events,service_events", "replicas": 3},
			{"alias": "ap-south", "bootstrap_servers": "10.88.2.11:9092", "topics": "analytics_events", "replicas": 3, "emit_checkpoints": false},
		},
	})
	values := map[string]string{}
	for line := range strings.SplitSeq(props, "\n") {
		key, value, ok := strings.Cut(line, "=")
		if ok && !strings.HasPrefix(strings.TrimSpace(line), "#") {
			values[strings.TrimSpace(key)] = strings.TrimSpace(value)
		}
	}
	for _, flow := range []string{"us-east->eu-west", "ap-south->eu-west"} {
		if values[flow+".enabled"] != "true" {
			t.Fatalf("flow %s not rendered:\n%s", flow, props)
		}
		if got := values[flow+".sync.topic.configs.enabled"]; got != "false" {
			t.Errorf("%s.sync.topic.configs.enabled = %q, want false", flow, got)
		}
	}
}

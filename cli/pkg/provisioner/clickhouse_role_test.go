package provisioner

import (
	"context"
	"strings"
	"testing"

	"frameworks/cli/pkg/inventory"

	"gopkg.in/yaml.v3"
)

func TestClickHouseRoleUsesScopedDeb822Repository(t *testing.T) {
	install := readRepoFile(t, "ansible/collections/ansible_collections/frameworks/infra/roles/clickhouse/tasks/install-debian.yml")
	for _, want := range []string{
		"ansible.builtin.deb822_repository",
		"https://packages.clickhouse.com/deb",
		"signed_by: \"{{ clickhouse_repository_key_url }}\"",
		"python3-debian",
		"allow_downgrades: true",
	} {
		if !strings.Contains(install, want) {
			t.Fatalf("ClickHouse Debian installer missing %q:\n%s", want, install)
		}
	}
	for _, forbidden := range []string{
		"ansible.builtin.apt_key",
		"ansible.builtin.apt_repository",
	} {
		if strings.Contains(install, forbidden) {
			t.Fatalf("ClickHouse Debian installer still uses %q:\n%s", forbidden, install)
		}
	}

	wrapper := readRepoFile(t, "ansible/collections/ansible_collections/frameworks/infra/roles/clickhouse/tasks/install.yml")
	if strings.Contains(wrapper, "tasks_from: install-Debian.yml") {
		t.Fatalf("ClickHouse wrapper still imports the legacy upstream installer:\n%s", wrapper)
	}
	if strings.Contains(wrapper, "ansible.builtin.apt:\n        name: clickhouse-keeper") {
		t.Fatalf("ClickHouse wrapper installs the Keeper package that conflicts with clickhouse-server:\n%s", wrapper)
	}
	if !strings.Contains(install, "/lib/systemd/system/clickhouse-server.service") {
		t.Fatalf("ClickHouse Debian installer does not verify the server unit:\n%s", install)
	}

	molecule := readRepoFile(t, "ansible/collections/ansible_collections/frameworks/infra/roles/clickhouse/molecule/default/molecule.yml")
	for _, image := range []string{
		"geerlingguy/docker-ubuntu2404-ansible:latest",
		"geerlingguy/docker-ubuntu2604-ansible:latest",
	} {
		if !strings.Contains(molecule, image) {
			t.Fatalf("ClickHouse Molecule scenario missing %q:\n%s", image, molecule)
		}
	}
}

// idealista.clickhouse defaults to trace level and ten 1000M files, which
// fills a data host's log disk within days.
func TestClickHouseRoleBoundsTheServerLog(t *testing.T) {
	var tasks []struct {
		Name string         `yaml:"name"`
		Vars map[string]any `yaml:"vars"`
	}
	install := readRepoFile(t, "ansible/collections/ansible_collections/frameworks/infra/roles/clickhouse/tasks/install.yml")
	if err := yaml.Unmarshal([]byte(install), &tasks); err != nil {
		t.Fatalf("parse install.yml: %v", err)
	}
	for _, task := range tasks {
		if task.Name != "Configure ClickHouse via idealista.clickhouse" {
			continue
		}
		logger, ok := task.Vars["clickhouse_logger"].(map[string]any)
		if !ok {
			t.Fatalf("idealista.clickhouse import does not override clickhouse_logger: %v", task.Vars)
		}
		if logger["level"] != "information" || logger["size"] != "200M" || logger["count"] != 10 {
			t.Fatalf("clickhouse_logger = %v, want information, 200M, 10", logger)
		}
		for _, key := range []string{"log", "errorlog", "console"} {
			if _, ok := logger[key]; !ok {
				t.Fatalf("clickhouse_logger replaces the upstream map and must set %s: %v", key, logger)
			}
		}
		return
	}
	t.Fatal("install.yml no longer imports idealista.clickhouse config")
}

func TestClickHouseRoleVarsUsesSharedCredentials(t *testing.T) {
	config := ServiceConfig{
		Version: "24.8.9.95",
		Port:    9000,
		Metadata: map[string]any{
			"clickhouse_password":          "writer-pass",
			"clickhouse_readonly_password": "reader-pass",
			"databases":                    []string{"periscope"},
		},
	}

	vars, err := clickhouseRoleVars(context.Background(), inventory.Host{}, config, RoleBuildHelpers{})
	if err != nil {
		t.Fatalf("clickhouseRoleVars: %v", err)
	}

	if got := vars["clickhouse_default_password"]; got != "writer-pass" {
		t.Fatalf("clickhouse_default_password = %v, want writer-pass", got)
	}
	if got := vars["clickhouse_readonly_password"]; got != "reader-pass" {
		t.Fatalf("clickhouse_readonly_password = %v, want reader-pass", got)
	}
	dbs, ok := vars["clickhouse_databases"].([]string)
	if !ok || len(dbs) != 1 || dbs[0] != "periscope" {
		t.Fatalf("clickhouse_databases = %#v, want [periscope]", vars["clickhouse_databases"])
	}
}

func TestClickHouseRoleVarsPassesNamedCollections(t *testing.T) {
	collections := []map[string]any{
		{
			"name": "quartermaster_pg",
			"settings": map[string]any{
				"host":     "10.66.0.10",
				"port":     5432,
				"database": "quartermaster",
				"user":     "frameworks_analytics_ro",
				"password": "secret",
			},
		},
	}
	config := ServiceConfig{
		Version: "26.3.10.62",
		Metadata: map[string]any{
			"named_collections":             collections,
			"clickhouse_analytics_password": "metabase-secret",
		},
	}

	vars, err := clickhouseRoleVars(context.Background(), inventory.Host{}, config, RoleBuildHelpers{})
	if err != nil {
		t.Fatalf("clickhouseRoleVars: %v", err)
	}
	got, ok := vars["clickhouse_named_collections"].([]map[string]any)
	if !ok || len(got) != 1 || got[0]["name"] != "quartermaster_pg" {
		t.Fatalf("clickhouse_named_collections = %#v, want the quartermaster_pg collection", vars["clickhouse_named_collections"])
	}
	if vars["clickhouse_analytics_password"] != "metabase-secret" {
		t.Fatalf("clickhouse_analytics_password not passed through: %#v", vars["clickhouse_analytics_password"])
	}

	// Absent metadata must leave the var unset so the ansible default ([])
	// removes a previously managed drop-in.
	vars, err = clickhouseRoleVars(context.Background(), inventory.Host{}, ServiceConfig{Version: "26.3.10.62", Metadata: map[string]any{}}, RoleBuildHelpers{})
	if err != nil {
		t.Fatalf("clickhouseRoleVars: %v", err)
	}
	if _, ok := vars["clickhouse_named_collections"]; ok {
		t.Fatalf("clickhouse_named_collections should be unset without metadata")
	}
}

func TestClickHouseRoleVarsResolvesVersionFromReleaseManifest(t *testing.T) {
	repo := writeTestGitopsRelease(t, `
platform_version: v9.9.9
infrastructure:
  - name: clickhouse
    version: "26.3.10.62"
    image: clickhouse/clickhouse-server:26.3.10.62
    digest: sha256:clickhousedigest
`)

	vars, err := clickhouseRoleVars(context.Background(), inventory.Host{}, ServiceConfig{
		Version: "stable",
		Metadata: map[string]any{
			"gitops_repository": repo,
			"platform_channel":  "stable",
		},
	}, RoleBuildHelpers{})
	if err != nil {
		t.Fatalf("clickhouseRoleVars: %v", err)
	}
	if got := vars["clickhouse_version"]; got != "26.3.10.62" {
		t.Fatalf("clickhouse_version = %v, want 26.3.10.62", got)
	}
}

func TestClickHouseRoleVarsDefaultsListenHostsToLocalAndMesh(t *testing.T) {
	config := ServiceConfig{
		Version: "26.3.10.62",
		Metadata: map[string]any{
			"advertised_host": "10.66.0.12",
		},
	}

	vars, err := clickhouseRoleVars(context.Background(), inventory.Host{}, config, RoleBuildHelpers{})
	if err != nil {
		t.Fatalf("clickhouseRoleVars: %v", err)
	}
	listenHosts, ok := vars["clickhouse_listen_hosts"].([]string)
	if !ok {
		t.Fatalf("clickhouse_listen_hosts = %#v, want []string", vars["clickhouse_listen_hosts"])
	}
	want := []string{"127.0.0.1", "10.66.0.12"}
	if len(listenHosts) != len(want) {
		t.Fatalf("clickhouse_listen_hosts = %#v, want %#v", listenHosts, want)
	}
	for i := range want {
		if listenHosts[i] != want[i] {
			t.Fatalf("clickhouse_listen_hosts = %#v, want %#v", listenHosts, want)
		}
	}
}

func TestClickHouseRoleVarsRespectsExplicitListenHost(t *testing.T) {
	config := ServiceConfig{
		Version: "26.3.10.62",
		Metadata: map[string]any{
			"advertised_host": "10.66.0.12",
			"listen_host":     "::",
		},
	}

	vars, err := clickhouseRoleVars(context.Background(), inventory.Host{}, config, RoleBuildHelpers{})
	if err != nil {
		t.Fatalf("clickhouseRoleVars: %v", err)
	}
	if got := vars["clickhouse_listen_host"]; got != "::" {
		t.Fatalf("clickhouse_listen_host = %v, want ::", got)
	}
	if _, ok := vars["clickhouse_listen_hosts"]; ok {
		t.Fatalf("clickhouse_listen_hosts should not be set when listen_host is explicit")
	}
}

// idealista.clickhouse appends "(event_date)" to the part_log and
// query_thread_log partition_by values while its defaults already read
// toYYYYMM(event_date); ClickHouse then rejects toYYYYMM(event_date)(event_date)
// on every system.part_log flush and writes the stack trace to the error log.
func TestClickHouseRoleOverridesBrokenSystemLogPartitionKeys(t *testing.T) {
	task := roleTaskByName(t, "ansible/collections/ansible_collections/frameworks/infra/roles/clickhouse/tasks/install.yml",
		"Write managed ClickHouse system log partition keys")
	spec, ok := moduleSpec(task, "copy")
	if !ok {
		t.Fatalf("system log partition override is not a copy task: %v", task)
	}
	if spec["dest"] != "/etc/clickhouse-server/config.d/system-log-partitions.xml" {
		t.Fatalf("override must be a config.d drop-in: %v", spec["dest"])
	}
	content, _ := spec["content"].(string)
	for _, table := range []string{"part_log", "query_thread_log"} {
		want := "<" + table + ">\n    <partition_by>toYYYYMM(event_date)</partition_by>\n  </" + table + ">"
		if !strings.Contains(content, want) {
			t.Errorf("drop-in does not set %s partition_by to toYYYYMM(event_date):\n%s", table, content)
		}
	}
	if task["notify"] != "clickhouse restart" {
		t.Errorf("system log tables are created at startup; the drop-in must notify clickhouse restart: %v", task["notify"])
	}
}

// idealista.clickhouse re-renders config.xml with its openSSL block on every
// run; stripping the block from config.xml afterwards changed the file, and
// restarted ClickHouse, on every apply. A config.d drop-in removes the block
// from the merged configuration instead and leaves config.xml as rendered.
func TestClickHouseRoleRemovesOpenSSLThroughADropIn(t *testing.T) {
	const file = "ansible/collections/ansible_collections/frameworks/infra/roles/clickhouse/tasks/install.yml"
	var tasks []any
	if err := yaml.Unmarshal([]byte(readRepoFile(t, file)), &tasks); err != nil {
		t.Fatal(err)
	}
	walkRoleBlocks(tasks, nil, func(task map[string]any, _ []map[string]any) {
		if spec, ok := moduleSpec(task, "replace"); ok && stringValue(spec["path"]) == "/etc/clickhouse-server/config.xml" {
			t.Errorf("%q edits the config.xml idealista.clickhouse renders", task["name"])
		}
	})
	disabled := roleTaskByName(t, file, "Remove the openSSL block through a drop-in when ClickHouse TLS is disabled")
	spec, _ := moduleSpec(disabled, "copy")
	if spec["dest"] != "/etc/clickhouse-server/config.d/no-openssl.xml" || !strings.Contains(stringValue(spec["content"]), `<openSSL remove="remove"/>`) {
		t.Fatalf("drop-in must remove openSSL from the merged config: %v", spec)
	}
	enabled := roleTaskByName(t, file, "Remove the openSSL removal drop-in when ClickHouse TLS is enabled")
	if spec, _ := moduleSpec(enabled, "file"); spec["state"] != "absent" {
		t.Fatalf("TLS-enabled hosts must keep openSSL: %v", spec)
	}
}

// idealista.clickhouse notifies its own Restart-clickhouse handler when it
// renders config.xml or users.xml. Unless that notification is routed to this
// role's restart handler, a run that changes both idealista's files and a
// config.d drop-in restarts ClickHouse twice in one flush.
func TestClickHouseRoleConfigChangesNotifyOneRestartHandler(t *testing.T) {
	const role = "ansible/collections/ansible_collections/frameworks/infra/roles/clickhouse/"
	idealista := roleTaskByName(t, role+"tasks/install.yml", "Configure ClickHouse via idealista.clickhouse")
	vars, _ := idealista["vars"].(map[string]any)
	if vars["clickhouse_handler_on_config_change"] != "clickhouse restart" {
		t.Fatalf("idealista.clickhouse config changes must notify clickhouse restart, got %v", vars["clickhouse_handler_on_config_change"])
	}

	var handlers []any
	if err := yaml.Unmarshal([]byte(readRepoFile(t, role+"handlers/main.yml")), &handlers); err != nil {
		t.Fatal(err)
	}
	var serverRestarts []map[string]any
	walkRoleBlocks(handlers, nil, func(handler map[string]any, _ []map[string]any) {
		if spec, ok := moduleSpec(handler, "systemd"); ok && spec["name"] == "{{ clickhouse_service_name }}" && spec["state"] == "restarted" {
			serverRestarts = append(serverRestarts, handler)
		}
		if includedTasks(handler) == clickhouseRestartTasks {
			serverRestarts = append(serverRestarts, handler)
		}
	})
	if len(serverRestarts) != 1 || serverRestarts[0]["listen"] != "clickhouse restart" {
		t.Fatalf("want one clickhouse-server restart handler listening on clickhouse restart, got %v", serverRestarts)
	}

	allowed := map[string]bool{"clickhouse restart": true, "keeper restart": true, "systemd daemon-reload": true}
	var tasks []any
	if err := yaml.Unmarshal([]byte(readRepoFile(t, role+"tasks/install.yml")), &tasks); err != nil {
		t.Fatal(err)
	}
	notifying := 0
	walkRoleBlocks(tasks, nil, func(task map[string]any, _ []map[string]any) {
		var topics []any
		switch notify := task["notify"].(type) {
		case nil:
			return
		case string:
			topics = []any{notify}
		case []any:
			topics = notify
		}
		notifying++
		for _, topic := range topics {
			if !allowed[stringValue(topic)] {
				t.Errorf("%q notifies %v, which is not a handler of this role", task["name"], topic)
			}
		}
	})
	if notifying == 0 {
		t.Fatal("install.yml has no notifying tasks; the scan did not reach the config tasks")
	}
}

const clickhouseRestartTasks = "_restart_server.yml"

// clickhouseUnitRestartSec is RestartSec in the clickhouse-server unit the
// ClickHouse packages ship.
const clickhouseUnitRestartSec = 30

func includedTasks(task map[string]any) string {
	for _, key := range []string{"ansible.builtin.include_tasks", "include_tasks"} {
		switch v := task[key].(type) {
		case string:
			return v
		case map[string]any:
			return stringValue(v["file"])
		}
	}
	return ""
}

// A ClickHouse start that never sends READY=1 fails the systemd start job with
// result "protocol" while Restart=always brings the server up RestartSec later.
// The restart must wait on the unit and a local query across that automatic
// restart instead of failing the apply on the first start job, and must not
// block on a start job that TimeoutStartSec=0 never times out.
func TestClickHouseRoleRestartOutlastsSystemdRestartCycle(t *testing.T) {
	const role = "ansible/collections/ansible_collections/frameworks/infra/roles/clickhouse/"

	var handlers []any
	if err := yaml.Unmarshal([]byte(readRepoFile(t, role+"handlers/main.yml")), &handlers); err != nil {
		t.Fatal(err)
	}
	restartHandler := false
	walkRoleBlocks(handlers, nil, func(handler map[string]any, _ []map[string]any) {
		if spec, ok := moduleSpec(handler, "systemd"); ok && spec["name"] == "{{ clickhouse_service_name }}" && spec["state"] == "restarted" {
			t.Errorf("handler %q restarts clickhouse-server through the start job instead of %s", handler["name"], clickhouseRestartTasks)
		}
		if handler["listen"] == "clickhouse restart" && includedTasks(handler) == clickhouseRestartTasks {
			restartHandler = true
		}
	})
	if !restartHandler {
		t.Errorf("the clickhouse restart handler does not run %s", clickhouseRestartTasks)
	}
	if !strings.Contains(readRepoFile(t, role+"tasks/restart.yml"), "include_tasks: "+clickhouseRestartTasks) {
		t.Errorf("tasks/restart.yml does not run %s", clickhouseRestartTasks)
	}

	var top []map[string]any
	if err := yaml.Unmarshal([]byte(readRepoFile(t, role+"tasks/"+clickhouseRestartTasks)), &top); err != nil {
		t.Fatal(err)
	}
	if len(top) != 1 {
		t.Fatalf("%s must be one block with a rescue, got %d top-level tasks", clickhouseRestartTasks, len(top))
	}
	block, _ := top[0]["block"].([]any)
	rescue, _ := top[0]["rescue"].([]any)

	step := map[string]int{}
	for i, item := range block {
		task, _ := item.(map[string]any)
		if spec, ok := moduleSpec(task, "systemd"); ok && spec["name"] == "{{ clickhouse_service_name }}" {
			switch spec["state"] {
			case "stopped":
				step["stop"] = i + 1
			case "started":
				if spec["no_block"] != true {
					t.Errorf("start %q waits on the start job; TimeoutStartSec=0 lets a start that never sends READY=1 block forever", task["name"])
				}
				step["start"] = i + 1
			case "restarted":
				t.Errorf("%q restarts through the start job", task["name"])
			}
		}
		if spec, ok := moduleSpec(task, "command"); ok {
			argv, _ := spec["argv"].([]any)
			waited := asInt(task["retries"]) * asInt(task["delay"])
			switch {
			case len(argv) > 1 && argv[0] == "systemctl" && argv[1] == "is-active":
				if !strings.Contains(stringValue(task["until"]), "'active'") {
					t.Errorf("%q does not retry until the unit is active: until=%v", task["name"], task["until"])
				}
				// A failed first start, RestartSec, and the automatic start.
				if waited <= 2*clickhouseUnitRestartSec {
					t.Errorf("%q waits %ds; it must outlast a failed start plus the unit's %ds RestartSec", task["name"], waited, clickhouseUnitRestartSec)
				}
				step["active"] = i + 1
			case len(argv) > 0 && argv[0] == "clickhouse-client":
				if !strings.Contains(stringValue(task["until"]), "rc == 0") || waited == 0 {
					t.Errorf("%q does not retry the local query: until=%v retries=%v delay=%v", task["name"], task["until"], task["retries"], task["delay"])
				}
				step["query"] = i + 1
			}
		}
		if includedTasks(task) == "_probe_client.yml" {
			step["probe"] = i + 1
		}
	}
	order := []string{"stop", "start", "active", "query", "probe"}
	for i, name := range order {
		if step[name] == 0 {
			t.Errorf("%s has no %s step", clickhouseRestartTasks, name)
			continue
		}
		if i > 0 && step[order[i-1]] != 0 && step[order[i-1]] > step[name] {
			t.Errorf("%s runs %s before %s", clickhouseRestartTasks, name, order[i-1])
		}
	}

	var diagnostics []string
	for _, item := range rescue {
		task, _ := item.(map[string]any)
		if cmd, ok := task["ansible.builtin.command"].(string); ok {
			diagnostics = append(diagnostics, cmd)
		}
		if spec, ok := moduleSpec(task, "fail"); ok {
			diagnostics = append(diagnostics, stringValue(spec["msg"]))
		}
	}
	joined := strings.Join(diagnostics, "\n")
	for _, want := range []string{"systemctl status", "journalctl -u", "clickhouse_restart_status.stdout", "clickhouse_restart_journal.stdout"} {
		if !strings.Contains(joined, want) {
			t.Errorf("a failed restart does not report %q:\n%s", want, joined)
		}
	}
}

// ClickHouse waits shutdown_wait_unfinished seconds (default 5) for open
// connections on stop and then exits without finishing its shutdown.
func TestClickHouseRoleWaitsForConnectionsOnShutdown(t *testing.T) {
	task := roleTaskByName(t, "ansible/collections/ansible_collections/frameworks/infra/roles/clickhouse/tasks/install.yml",
		"Write managed ClickHouse shutdown wait")
	spec, ok := moduleSpec(task, "copy")
	if !ok || spec["dest"] != "/etc/clickhouse-server/config.d/shutdown.xml" {
		t.Fatalf("shutdown wait must be a config.d drop-in: %v", task)
	}
	if !strings.Contains(stringValue(spec["content"]), "<shutdown_wait_unfinished>60</shutdown_wait_unfinished>") {
		t.Fatalf("drop-in does not raise shutdown_wait_unfinished to 60s:\n%v", spec["content"])
	}
	if _, ok := task["notify"]; ok {
		t.Fatalf("the value is read at startup; writing it must not restart ClickHouse: %v", task["notify"])
	}
}

func asInt(v any) int {
	switch n := v.(type) {
	case int:
		return n
	case float64:
		return int(n)
	}
	return 0
}

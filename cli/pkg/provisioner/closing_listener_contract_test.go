package provisioner

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func roleFile(t *testing.T, role, rel string) string {
	t.Helper()
	_, current, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	path := filepath.Join(filepath.Dir(current), "..", "..", "..", "ansible", "collections", "ansible_collections", "frameworks", "infra", "roles", role, rel)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(data)
}

type closingListenerTask struct {
	Name        string         `yaml:"name"`
	Notify      any            `yaml:"notify"`
	ChangedWhen any            `yaml:"changed_when"`
	Copy        map[string]any `yaml:"ansible.builtin.copy"`
	Template    map[string]any `yaml:"ansible.builtin.template"`
}

func closingListenerTasks(t *testing.T, role, file string) []closingListenerTask {
	t.Helper()
	var tasks []closingListenerTask
	if err := yaml.Unmarshal([]byte(roleFile(t, role, filepath.Join("tasks", file))), &tasks); err != nil {
		t.Fatalf("parse %s/%s: %v", role, file, err)
	}
	return tasks
}

// TestClickHouseCompatProtocolDropInDoesNotRestart pins that the drop-in
// removing ClickHouse's MySQL and PostgreSQL listeners notifies no handler:
// a running server closes them at its next start, not because of the role.
func TestClickHouseCompatProtocolDropInDoesNotRestart(t *testing.T) {
	for _, task := range closingListenerTasks(t, "clickhouse", "install.yml") {
		if task.Copy == nil || task.Copy["dest"] != "/etc/clickhouse-server/config.d/no-compat-protocols.xml" {
			continue
		}
		content, _ := task.Copy["content"].(string)
		for _, want := range []string{`<mysql_port remove="remove"/>`, `<postgresql_port remove="remove"/>`} {
			if !strings.Contains(content, want) {
				t.Fatalf("protocol drop-in lacks %s:\n%s", want, content)
			}
		}
		if task.Notify != nil {
			t.Fatalf("protocol drop-in notifies %v; it must take effect at the server's next start without restarting it", task.Notify)
		}
		return
	}
	t.Fatal("no task writes config.d/no-compat-protocols.xml")
}

// TestYugabyteDeferredTserverFlagsDoNotRestart pins the three places that
// share the deferred marker: the role's variable equals the CLI constant,
// the render task counts only the flags above it as a change, and the
// applied-configuration receipt hashes only the lines above it.
func TestYugabyteDeferredTserverFlagsDoNotRestart(t *testing.T) {
	var vars map[string]any
	if err := yaml.Unmarshal([]byte(roleFile(t, "yugabyte", filepath.Join("vars", "main.yml"))), &vars); err != nil {
		t.Fatal(err)
	}
	if got := vars["yugabyte_tserver_deferred_marker"]; got != YugabyteTserverDeferredMarker {
		t.Fatalf("role marker = %q, want %q", got, YugabyteTserverDeferredMarker)
	}
	flags, _ := vars["yugabyte_tserver_deferred_flags"].([]any)
	if len(flags) == 0 || flags[0] != "--start_cql_proxy=false" {
		t.Fatalf("deferred flags = %v, want YCQL off", flags)
	}
	if !strings.Contains(roleFile(t, "yugabyte", filepath.Join("templates", "tserver.conf.j2")), "{{ yugabyte_tserver_deferred_marker }}") {
		t.Fatal("tserver.conf.j2 does not render the deferred marker")
	}
	var render *closingListenerTask
	for _, task := range closingListenerTasks(t, "yugabyte", "configure.yml") {
		if task.Template != nil && task.Template["src"] == "tserver.conf.j2" {
			render = &task
			break
		}
	}
	if render == nil {
		t.Fatal("no task renders tserver.conf.j2")
	}
	changedWhen, _ := render.ChangedWhen.(string)
	if !strings.Contains(changedWhen, ".split(yugabyte_tserver_deferred_marker)[0]") {
		t.Fatalf("tserver render changed_when = %q; a deferred-only change must not count as a change", changedWhen)
	}
	record := roleFile(t, "yugabyte", filepath.Join("tasks", "record-tserver-config.yml"))
	if !strings.Contains(record, "yugabyte_tserver_deferred_marker") || !strings.Contains(record, "$0 == marker { exit }") {
		t.Fatalf("the applied-configuration receipt does not stop at the deferred marker:\n%s", record)
	}
}

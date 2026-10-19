package provisioner

import (
	"fmt"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"testing"
)

// The release precheck runs every role under --check and converges a host only
// when a task would change. Ansible skips command and shell tasks in check mode
// and registers an empty result, so a read-only probe whose result decides
// whether a changing task runs must run there too (check_mode: false);
// otherwise the precheck decides on empty output and reports drift the real
// run never applies, or misses drift it would.
func TestReadOnlyProbesDecidingChangesRunUnderCheckMode(t *testing.T) {
	_, current, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("cannot locate test file")
	}
	roles := filepath.Join(filepath.Dir(current), "..", "..", "..", "ansible", "collections", "ansible_collections", "frameworks", "infra", "roles")
	var violations []string
	for _, file := range loadRoleTaskFiles(t, roles) {
		for _, v := range checkModeProbeViolations(flattenTasksWithConditions(file.tasks, "")) {
			violations = append(violations, file.role+"/"+filepath.Base(file.path)+": "+v)
		}
	}
	sort.Strings(violations)
	if len(violations) > 0 {
		t.Fatalf("read-only probes that decide a change are skipped under --check (add check_mode: false):\n%s", strings.Join(violations, "\n"))
	}
}

type conditionedTask struct {
	task map[string]any
	// when is the task's own condition joined with every enclosing block's.
	when string
}

func flattenTasksWithConditions(tasks []any, inherited string) []conditionedTask {
	var out []conditionedTask
	for _, item := range tasks {
		task, ok := item.(map[string]any)
		if !ok {
			continue
		}
		when := strings.TrimSpace(inherited + " " + fmt.Sprint(task["when"]))
		nested := false
		for _, key := range []string{"block", "rescue", "always"} {
			if children, ok := task[key].([]any); ok {
				nested = true
				out = append(out, flattenTasksWithConditions(children, when)...)
			}
		}
		if !nested {
			out = append(out, conditionedTask{task: task, when: when})
		}
	}
	return out
}

var checkModeInertModules = []string{"assert", "debug", "fail", "set_fact", "meta"}

func taskModuleIsOneOf(task map[string]any, short []string) bool {
	for _, name := range short {
		if _, ok := task["ansible.builtin."+name]; ok {
			return true
		}
		if _, ok := task[name]; ok {
			return true
		}
	}
	return false
}

func checkModeProbeViolations(tasks []conditionedTask) []string {
	var out []string
	for _, probe := range tasks {
		register, _ := probe.task["register"].(string)
		if register == "" || probe.task["changed_when"] != false || probe.task["check_mode"] == false ||
			!taskModuleIsOneOf(probe.task, []string{"command", "shell"}) || strings.Contains(probe.when, "not ansible_check_mode") {
			continue
		}
		ref := regexp.MustCompile(`\b` + regexp.QuoteMeta(register) + `\b`)
		for _, consumer := range tasks {
			if consumer.task["check_mode"] == false || taskModuleIsOneOf(consumer.task, checkModeInertModules) ||
				strings.Contains(consumer.when, "not ansible_check_mode") || !ref.MatchString(consumer.when) {
				continue
			}
			out = append(out, fmt.Sprintf("%q registers %s, which decides %q", probe.task["name"], register, consumer.task["name"]))
		}
	}
	return out
}

func TestCheckModeProbeScanFlagsEachForm(t *testing.T) {
	probe := map[string]any{"name": "detect", "ansible.builtin.command": "lvs", "register": "lvm_probe", "changed_when": false}
	mask := map[string]any{"name": "mask", "ansible.builtin.systemd": map[string]any{"masked": true}}
	cases := []struct {
		name  string
		tasks []conditionedTask
		want  int
	}{
		{"probe deciding a change", []conditionedTask{{task: probe}, {task: mask, when: "lvm_probe.rc != 0"}}, 1},
		{"probe feeding an assert", []conditionedTask{{task: probe}, {task: map[string]any{"name": "a", "ansible.builtin.assert": nil}, when: "lvm_probe.rc == 0"}}, 0},
		{"consumer skipped in check mode", []conditionedTask{{task: probe}, {task: mask, when: "not ansible_check_mode and lvm_probe.rc != 0"}}, 0},
		{"probe gated off in check mode", []conditionedTask{{task: probe, when: "not ansible_check_mode"}, {task: mask, when: "lvm_probe.rc != 0"}}, 0},
		{"unrelated condition", []conditionedTask{{task: probe}, {task: mask, when: "other_probe.rc != 0"}}, 0},
	}
	for _, c := range cases {
		if got := len(checkModeProbeViolations(c.tasks)); got != c.want {
			t.Errorf("%s: %d violations, want %d", c.name, got, c.want)
		}
	}
	probe["check_mode"] = false
	if got := checkModeProbeViolations([]conditionedTask{{task: probe}, {task: mask, when: "lvm_probe.rc != 0"}}); len(got) != 0 {
		t.Errorf("check_mode: false runs the probe under --check: %v", got)
	}
}

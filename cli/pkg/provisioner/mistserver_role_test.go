package provisioner

import (
	"os"
	"slices"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

const (
	mistserverRoleDir = "ansible/collections/ansible_collections/frameworks/infra/roles/mistserver/"
	edgeRoleDir       = "ansible/collections/ansible_collections/frameworks/infra/roles/edge/"
)

// notifyTopics returns the handler topics a task notifies.
func notifyTopics(task map[string]any) []string {
	switch notify := task["notify"].(type) {
	case string:
		return []string{notify}
	case []any:
		var topics []string
		for _, topic := range notify {
			topics = append(topics, stringValue(topic))
		}
		return topics
	}
	return nil
}

// roleNotifiers maps every task name in the role's task files to the topics it
// notifies.
func roleNotifiers(t *testing.T, files ...string) map[string][]string {
	t.Helper()
	out := map[string][]string{}
	for _, file := range files {
		var tasks []any
		if err := yaml.Unmarshal([]byte(readRepoFile(t, file)), &tasks); err != nil {
			t.Fatalf("parse %s: %v", file, err)
		}
		walkRoleBlocks(tasks, nil, func(task map[string]any, _ []map[string]any) {
			if topics := notifyTopics(task); len(topics) > 0 {
				name := stringValue(task["name"])
				out[name] = append(out[name], topics...)
			}
		})
	}
	return out
}

// checkModeReports maps each handler topic to the names of the handlers that
// report it under check mode (the results `edge provision` classifies).
func checkModeReports(t *testing.T, handlerFiles ...string) map[string][]string {
	t.Helper()
	out := map[string][]string{}
	for _, file := range handlerFiles {
		var handlers []any
		if err := yaml.Unmarshal([]byte(readRepoFile(t, file)), &handlers); err != nil {
			t.Fatalf("parse %s: %v", file, err)
		}
		walkRoleBlocks(handlers, nil, func(handler map[string]any, _ []map[string]any) {
			if stringValue(handler["when"]) != "ansible_check_mode" {
				return
			}
			topic := stringValue(handler["listen"])
			out[topic] = append(out[topic], stringValue(handler["name"]))
		})
	}
	return out
}

// A hard MistServer restart drops every live stream on the node, and
// `edge provision` drains (or interrupts) the node for it. Only the changes
// the running controller cannot take without a restart may notify it: the
// controller interface/port reconcile and a changed start-time environment
// (env file assignments, unit Environment lines, launchd
// EnvironmentVariables), which the USR1 re-exec does not pick up. The
// renders themselves, and so ExecStart-style changes, notify the
// pending-restart handler.
func TestMistserverRoleHardRestartNotifiers(t *testing.T) {
	entries, err := os.ReadDir("../../../" + mistserverRoleDir + "tasks")
	if err != nil {
		t.Fatal(err)
	}
	var files []string
	for _, entry := range entries {
		if strings.HasSuffix(entry.Name(), ".yml") {
			files = append(files, mistserverRoleDir+"tasks/"+entry.Name())
		}
	}
	var hard []string
	for name, topics := range roleNotifiers(t, files...) {
		if slices.Contains(topics, "mistserver restart") && !slices.Contains(hard, name) {
			hard = append(hard, name)
		}
	}
	slices.Sort(hard)
	want := []string{
		"Reconcile MistServer controller config",
		"Report MistServer environment change",
		"Report MistServer launchd environment change",
		"Report MistServer unit environment change",
	}
	if !slices.Equal(hard, want) {
		t.Fatalf("tasks notifying the hard `mistserver restart` = %q, want %q", hard, want)
	}
}

// The tasks that render units and env files must classify as a non-
// interrupting plan: their check-mode handler reports may not include an
// interrupting one, or `edge provision` drains (or restarts) a live edge for
// a change its running processes do not need.
func TestEdgeUnitAndEnvRenderChangesAreNotInterrupting(t *testing.T) {
	renders := []string{
		"Render env file",
		"Render systemd unit",
		"Render launchd wrapper script",
		"Render launchd plist",
		"Render edge caddy env",
		"Render edge caddy systemd unit",
	}
	notifiers := roleNotifiers(t,
		mistserverRoleDir+"tasks/configure-linux.yml",
		mistserverRoleDir+"tasks/configure-darwin.yml",
		edgeRoleDir+"tasks/install-native-linux-caddy.yml",
	)
	reports := checkModeReports(t, mistserverRoleDir+"handlers/main.yml", edgeRoleDir+"handlers/main.yml")
	changed := slices.Clone(renders)
	for _, task := range renders {
		topics := notifiers[task]
		if len(topics) == 0 {
			t.Fatalf("render task %q notifies no handler", task)
		}
		for _, topic := range topics {
			if len(reports[topic]) == 0 {
				t.Fatalf("topic %q (from %q) has no check-mode report handler", topic, task)
			}
			changed = append(changed, reports[topic]...)
		}
	}
	got := ClassifyEdgeChanges(changed)
	if !got.Changed || got.Interrupting {
		t.Fatalf("unit/env render plan %q classified as %+v, want changed and not interrupting", changed, got)
	}
}

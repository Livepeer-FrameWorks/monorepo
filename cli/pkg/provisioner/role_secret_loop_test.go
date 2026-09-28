package provisioner

import (
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"
)

// Database entries carry their owner and runtime passwords, and every task
// result echoes its loop item, so a task may loop over the raw entries only
// under no_log. Otherwise it loops over the fields it needs.
func TestRoleTasksNeverEchoDatabaseEntries(t *testing.T) {
	_, current, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("cannot locate test file")
	}
	roles := filepath.Join(filepath.Dir(current), "..", "..", "..", "ansible", "collections", "ansible_collections", "frameworks", "infra", "roles")
	rawLoop := regexp.MustCompile(`^\s*loop:\s*"\{\{\s*[a-z_]+_databases\s*(\||\}\})`)
	files, err := filepath.Glob(filepath.Join(roles, "*", "tasks", "*.yml"))
	if err != nil || len(files) == 0 {
		t.Fatalf("no role task files found under %s: %v", roles, err)
	}
	for _, file := range files {
		data, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		for _, task := range strings.Split(string(data), "\n- name:") {
			name := strings.SplitN(task, "\n", 2)[0]
			for _, line := range strings.Split(task, "\n") {
				if rawLoop.MatchString(line) && !strings.Contains(task, "no_log: true") && !strings.Contains(line, "clickhouse_databases") {
					t.Errorf("%s: task %q loops over raw database entries without no_log: %s", filepath.Base(filepath.Dir(filepath.Dir(file)))+"/"+filepath.Base(file), strings.TrimSpace(name), strings.TrimSpace(line))
				}
			}
		}
	}
}

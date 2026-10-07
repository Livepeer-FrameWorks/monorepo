package provisioner

import (
	"path/filepath"
	"strings"
	"testing"
)

// Install archives a role downloads to /tmp, and the /tmp directories it
// extracts them into, are removed in the always section of the block that
// creates them, so neither a finished nor a failed install leaves them behind.
// That block carries no when: its always section also removes, and reports
// under --check, what an earlier run left behind on runs that install nothing.
func TestRoleInstallTemporaryFilesAreRemovedInAlways(t *testing.T) {
	roles := ansibleTreePath(t, "collections", "ansible_collections", "frameworks", "infra", "roles")
	files := loadRoleTaskFiles(t, roles)
	roleVars := map[string]map[string]any{}
	checked := map[string]bool{}
	for _, file := range files {
		vars, seen := roleVars[file.role]
		if !seen {
			vars = loadRoleDefaults(t, filepath.Join(roles, file.role, "defaults", "main.yml"))
			for key, value := range loadRoleDefaults(t, filepath.Join(roles, file.role, "vars", "main.yml")) {
				vars[key] = value
			}
			roleVars[file.role] = vars
		}
		fileVars := map[string]any{}
		for key, value := range vars {
			fileVars[key] = value
		}
		collectSetFacts(file.tasks, fileVars)

		walkRoleBlocks(file.tasks, nil, func(task map[string]any, enclosing []map[string]any) {
			for _, module := range []string{"get_url", "unarchive", "file"} {
				spec, ok := moduleSpec(task, module)
				if !ok {
					continue
				}
				key := "dest"
				if module == "file" {
					if spec["state"] != "directory" {
						continue
					}
					key = "path"
				}
				raw, ok := spec[key].(string)
				if !ok {
					continue
				}
				resolved := resolveRoleVar(raw, fileVars)
				if resolved != "/tmp" && !strings.HasPrefix(resolved, "/tmp/") {
					continue
				}
				where := file.role + "/" + filepath.Base(file.path) + ": " + stringValue(task["name"])
				checked[where] = true
				switch block := removedInAlways(enclosing, raw); {
				case block == nil:
					t.Errorf("%s writes %s under /tmp but no enclosing block removes it in always", where, raw)
				case block["when"] != nil:
					t.Errorf("%s: the block removing %s has a when, so a leftover from an earlier run survives runs that skip it", where, raw)
				}
			}
		})
	}
	for _, want := range []string{
		"go_service/install.yml: Download pinned artifact",
		"yugabyte/install.yml: Download pinned YugabyteDB tarball",
		"mistserver/install-linux.yml: Download MistServer tarball",
		"helmsman/install-linux.yml: Download Helmsman asset",
		"edge/install-native-linux-caddy.yml: Download Caddy tarball",
		"prometheus_stack/victoriametrics.yml: Extract VictoriaMetrics",
	} {
		if !checked[want] {
			t.Errorf("scan did not reach %q; checked %d task(s)", want, len(checked))
		}
	}
}

func moduleSpec(task map[string]any, module string) (map[string]any, bool) {
	for _, key := range []string{"ansible.builtin." + module, module} {
		if spec, ok := task[key].(map[string]any); ok {
			return spec, true
		}
	}
	return nil, false
}

// walkRoleBlocks visits every leaf task with the chain of blocks enclosing it,
// outermost first.
func walkRoleBlocks(tasks []any, enclosing []map[string]any, visit func(map[string]any, []map[string]any)) {
	for _, item := range tasks {
		task, ok := item.(map[string]any)
		if !ok {
			continue
		}
		nested := false
		for _, key := range []string{"block", "rescue", "always"} {
			if children, ok := task[key].([]any); ok {
				nested = true
				walkRoleBlocks(children, append(enclosing[:len(enclosing):len(enclosing)], task), visit)
			}
		}
		if !nested {
			visit(task, enclosing)
		}
	}
}

func collectSetFacts(tasks []any, vars map[string]any) {
	walkRoleBlocks(tasks, nil, func(task map[string]any, _ []map[string]any) {
		spec, ok := moduleSpec(task, "set_fact")
		if !ok {
			return
		}
		for key, value := range spec {
			if s, ok := value.(string); ok {
				vars[key] = s
			}
		}
	})
}

// removedInAlways returns the block in enclosing that removes path (by the
// same template expression) with a state=absent file task in its always list.
func removedInAlways(enclosing []map[string]any, path string) map[string]any {
	want := strings.TrimSpace(path)
	for _, block := range enclosing {
		always, ok := block["always"].([]any)
		if !ok {
			continue
		}
		found := false
		walkRoleBlocks(always, nil, func(task map[string]any, _ []map[string]any) {
			spec, ok := moduleSpec(task, "file")
			if !ok || spec["state"] != "absent" {
				return
			}
			target := strings.TrimSpace(stringValue(spec["path"]))
			if target == want {
				found = true
				return
			}
			if !strings.Contains(target, "item") {
				return
			}
			loop, _ := task["loop"].([]any)
			for _, entry := range loop {
				if strings.TrimSpace(stringValue(entry)) == want {
					found = true
				}
			}
		})
		if found {
			return block
		}
	}
	return nil
}

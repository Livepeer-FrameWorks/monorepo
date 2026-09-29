package provisioner

import (
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

var roleVarReference = regexp.MustCompile(`^\{\{\s*([A-Za-z_][A-Za-z0-9_]*)\s*\}\}$`)

type roleTaskFile struct {
	role  string
	path  string
	tasks []any
}

// Ansible stages module files under the become user's ~/.ansible/tmp. A
// system user created without a home cannot create that directory, so every
// task that becomes such a user must point ansible_remote_tmp at a directory
// the user owns.
func TestBecomeHomelessUserSetsWritableRemoteTmp(t *testing.T) {
	_, current, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("cannot locate test file")
	}
	roles := filepath.Join(filepath.Dir(current), "..", "..", "..", "ansible", "collections", "ansible_collections", "frameworks", "infra", "roles")
	files := loadRoleTaskFiles(t, roles)
	defaults := map[string]map[string]any{}
	for _, file := range files {
		if _, seen := defaults[file.role]; !seen {
			defaults[file.role] = loadRoleDefaults(t, filepath.Join(roles, file.role, "defaults", "main.yml"))
		}
	}

	homeless := map[string]string{}
	for _, file := range files {
		walkRoleTasks(file.tasks, roleTaskScope{}, func(task map[string]any, _ roleTaskScope) {
			spec, isUser := userModuleSpec(task)
			if !isUser || spec["create_home"] != false {
				return
			}
			name, ok := spec["name"].(string)
			if !ok {
				return
			}
			if resolved := resolveRoleVar(name, defaults[file.role]); resolved != "" {
				homeless[resolved] = file.role + "/" + filepath.Base(file.path)
			}
		})
	}
	for _, want := range []string{"kafka", "privateer", "frameworks"} {
		if _, ok := homeless[want]; !ok {
			t.Fatalf("homeless user %q not discovered from role user tasks; discovered %v", want, homelessUserNames(homeless))
		}
	}

	checked := 0
	for _, file := range files {
		walkRoleTasks(file.tasks, roleTaskScope{}, func(task map[string]any, scope roleTaskScope) {
			if scope.becomeUser == "" {
				return
			}
			user := resolveRoleVar(scope.becomeUser, defaults[file.role])
			origin, isHomeless := homeless[user]
			if !isHomeless {
				return
			}
			checked++
			if !scope.remoteTmp {
				t.Errorf("%s/%s: task %q becomes %s (created without a home in %s) but sets no ansible_remote_tmp",
					file.role, filepath.Base(file.path), task["name"], user, origin)
			}
		})
	}
	if checked == 0 {
		t.Fatal("no task becomes a homeless user; the scan found nothing to check")
	}
}

type roleTaskScope struct {
	becomeUser string
	remoteTmp  bool
}

func walkRoleTasks(tasks []any, parent roleTaskScope, visit func(map[string]any, roleTaskScope)) {
	for _, item := range tasks {
		task, ok := item.(map[string]any)
		if !ok {
			continue
		}
		scope := parent
		if user, ok := task["become_user"].(string); ok {
			scope.becomeUser = user
		}
		if vars, ok := task["vars"].(map[string]any); ok {
			if _, set := vars["ansible_remote_tmp"]; set {
				scope.remoteTmp = true
			}
		}
		nested := false
		for _, key := range []string{"block", "rescue", "always"} {
			if children, ok := task[key].([]any); ok {
				nested = true
				walkRoleTasks(children, scope, visit)
			}
		}
		if !nested {
			visit(task, scope)
		}
	}
}

func userModuleSpec(task map[string]any) (map[string]any, bool) {
	for _, key := range []string{"ansible.builtin.user", "user"} {
		if spec, ok := task[key].(map[string]any); ok {
			return spec, true
		}
	}
	return nil, false
}

func resolveRoleVar(value string, defaults map[string]any) string {
	for range 5 {
		match := roleVarReference.FindStringSubmatch(strings.TrimSpace(value))
		if match == nil {
			return value
		}
		next, ok := defaults[match[1]].(string)
		if !ok {
			return ""
		}
		value = next
	}
	return ""
}

func loadRoleTaskFiles(t *testing.T, roles string) []roleTaskFile {
	t.Helper()
	var files []roleTaskFile
	for _, pattern := range []string{"*/tasks/*.yml", "*/handlers/*.yml"} {
		paths, err := filepath.Glob(filepath.Join(roles, pattern))
		if err != nil {
			t.Fatal(err)
		}
		for _, path := range paths {
			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			var tasks []any
			if err := yaml.Unmarshal(data, &tasks); err != nil {
				t.Fatalf("parse %s: %v", path, err)
			}
			role := filepath.Base(filepath.Dir(filepath.Dir(path)))
			files = append(files, roleTaskFile{role: role, path: path, tasks: tasks})
		}
	}
	if len(files) == 0 {
		t.Fatalf("no role task files found under %s", roles)
	}
	return files
}

func loadRoleDefaults(t *testing.T, path string) map[string]any {
	t.Helper()
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return map[string]any{}
	}
	if err != nil {
		t.Fatal(err)
	}
	defaults := map[string]any{}
	if err := yaml.Unmarshal(data, &defaults); err != nil {
		t.Fatalf("parse %s: %v", path, err)
	}
	return defaults
}

func homelessUserNames(values map[string]string) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

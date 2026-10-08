package provisioner

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

const etcFrameworksDir = "/etc/frameworks"

// etcFrameworksOwners are the roles that install processes running as the
// frameworks user. Helmsman's component updater writes
// component-versions.env.tmp into /etc/frameworks and renames it, so the
// directory must be writable by frameworks wherever those processes run.
var etcFrameworksOwners = []string{"go_service", "helmsman", "mistserver"}

type etcFrameworksTask struct {
	where       string
	role        string
	owner       string
	group       string
	mode        string
	ownerIsSet  bool
	groupIsSet  bool
	resolvedErr string
}

var jinjaIdent = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

func roleDefaults(t *testing.T, root, role string) map[string]any {
	t.Helper()
	out := map[string]any{}
	b, err := os.ReadFile(filepath.Join(root, "roles", role, "defaults", "main.yml"))
	if err != nil {
		return out
	}
	if err := yaml.Unmarshal(b, &out); err != nil {
		t.Fatalf("parse %s defaults: %v", role, err)
	}
	return out
}

// resolveRoleValue evaluates the small subset of Jinja these tasks use for
// paths and ownership: a literal, "omit if ansible_check_mode else X",
// "item.<key> | default(X)", and a bare role default.
func resolveRoleValue(raw any, item map[string]any, defaults map[string]any) (string, bool) {
	s, ok := raw.(string)
	if !ok {
		return "", false
	}
	s = strings.TrimSpace(s)
	if !strings.HasPrefix(s, "{{") {
		return s, true
	}
	expr := strings.TrimSpace(strings.TrimSuffix(strings.TrimPrefix(s, "{{"), "}}"))
	expr = strings.TrimSpace(strings.TrimPrefix(expr, "omit if ansible_check_mode else "))
	if expr == "item" {
		v, isString := item[""].(string)
		return v, isString
	}
	if strings.HasPrefix(expr, "item.") {
		key, fallback, _ := strings.Cut(strings.TrimPrefix(expr, "item."), "|")
		if v, present := item[strings.TrimSpace(key)]; present {
			return resolveRoleValue(v, item, defaults)
		}
		fallback = strings.TrimSpace(fallback)
		if !strings.HasPrefix(fallback, "default(") {
			return "", false
		}
		expr = strings.TrimSpace(strings.TrimSuffix(strings.TrimPrefix(fallback, "default("), ")"))
	}
	if !jinjaIdent.MatchString(expr) {
		return "", false
	}
	v, ok := defaults[expr]
	if !ok {
		return "", false
	}
	return resolveRoleValue(v, item, defaults)
}

func collectEtcFrameworksTasks(t *testing.T) []etcFrameworksTask {
	t.Helper()
	root := "../../../ansible/collections/ansible_collections/frameworks/infra"
	files, err := filepath.Glob(filepath.Join(root, "roles", "*", "tasks", "*.yml"))
	if err != nil {
		t.Fatal(err)
	}
	var found []etcFrameworksTask
	for _, file := range files {
		rel, _ := filepath.Rel(root, file)
		role := strings.Split(rel, string(filepath.Separator))[1]
		defaults := roleDefaults(t, root, role)
		b, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		var tasks []any
		if err := yaml.Unmarshal(b, &tasks); err != nil {
			t.Fatalf("parse %s: %v", rel, err)
		}
		walkRoleBlocks(tasks, nil, func(task map[string]any, _ []map[string]any) {
			spec, ok := moduleSpec(task, "file")
			if !ok || spec["state"] != "directory" {
				return
			}
			items := []map[string]any{{}}
			if loop, ok := task["loop"].([]any); ok {
				items = items[:0]
				for _, entry := range loop {
					switch v := entry.(type) {
					case string:
						items = append(items, map[string]any{"": v})
					case map[string]any:
						items = append(items, v)
					}
				}
			}
			for _, item := range items {
				path, ok := resolveRoleValue(spec["path"], item, defaults)
				if !ok || path != etcFrameworksDir {
					continue
				}
				entry := etcFrameworksTask{where: rel + ": " + stringOf(task["name"]), role: role}
				entry.mode, _ = resolveRoleValue(spec["mode"], item, defaults)
				if raw, set := spec["owner"]; set {
					entry.ownerIsSet = true
					if entry.owner, ok = resolveRoleValue(raw, item, defaults); !ok {
						entry.resolvedErr = "owner " + stringOf(raw)
					}
				}
				if raw, set := spec["group"]; set {
					entry.groupIsSet = true
					if entry.group, ok = resolveRoleValue(raw, item, defaults); !ok {
						entry.resolvedErr = "group " + stringOf(raw)
					}
				}
				found = append(found, entry)
			}
		})
	}
	return found
}

func stringOf(v any) string {
	s, _ := v.(string)
	return s
}

// Every role that ensures /etc/frameworks agrees on frameworks:frameworks
// 0755. Only the roles running frameworks-user processes set the owner; the
// rest only ensure the directory exists, so no two roles flip its ownership
// on every apply.
func TestEtcFrameworksOwnershipIsConsistentAcrossRoles(t *testing.T) {
	tasks := collectEtcFrameworksTasks(t)

	seen := map[string]bool{}
	ownerRoles := map[string]bool{}
	for _, task := range tasks {
		seen[task.role] = true
		if task.resolvedErr != "" {
			t.Errorf("%s: cannot resolve %s", task.where, task.resolvedErr)
			continue
		}
		if task.mode != "0755" {
			t.Errorf("%s: /etc/frameworks mode = %q, want \"0755\"", task.where, task.mode)
		}
		if task.ownerIsSet != task.groupIsSet {
			t.Errorf("%s: /etc/frameworks must set owner and group together", task.where)
		}
		if task.ownerIsSet {
			ownerRoles[task.role] = true
			if task.owner != "frameworks" || task.group != "frameworks" {
				t.Errorf("%s: /etc/frameworks owner = %s:%s, want frameworks:frameworks", task.where, task.owner, task.group)
			}
		}
	}

	for _, role := range []string{"chatwoot", "edge", "go_service", "helmsman", "mistserver", "privateer", "redis"} {
		if !seen[role] {
			t.Errorf("role %s no longer ensures /etc/frameworks; update this contract", role)
		}
	}
	var gotOwners []string
	for role := range ownerRoles {
		gotOwners = append(gotOwners, role)
	}
	sort.Strings(gotOwners)
	if strings.Join(gotOwners, ",") != strings.Join(etcFrameworksOwners, ",") {
		t.Errorf("roles setting /etc/frameworks ownership = %v, want %v", gotOwners, etcFrameworksOwners)
	}
}

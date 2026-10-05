package provisioner

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// packageModules are the task modules that reach a package mirror.
var packageModules = map[string]bool{
	"ansible.builtin.apt": true, "apt": true,
	"ansible.builtin.package": true, "package": true,
}

// mirrorTask reports whether a package task fetches from a mirror: a cache
// refresh, or an install of named packages.
func mirrorTask(args map[string]any) bool {
	if v, ok := args["update_cache"]; ok && fmt.Sprint(v) == "true" {
		return true
	}
	if _, named := args["name"]; !named {
		if _, debs := args["deb"]; !debs {
			return false
		}
	}
	state := fmt.Sprint(args["state"])
	return args["state"] == nil || state == "present" || state == "latest"
}

func collectPackageTasks(tasks []map[string]any, file string, out *[]string) {
	for _, task := range tasks {
		for _, nested := range []string{"block", "rescue", "always"} {
			if inner, ok := task[nested].([]any); ok {
				var sub []map[string]any
				for _, item := range inner {
					if m, ok := item.(map[string]any); ok {
						sub = append(sub, m)
					}
				}
				collectPackageTasks(sub, file, out)
			}
		}
		for module := range packageModules {
			args, ok := task[module].(map[string]any)
			if !ok || !mirrorTask(args) {
				continue
			}
			var missing []string
			if n, ok := task["async"].(int); !ok || n <= 0 {
				missing = append(missing, "async")
			}
			if n, ok := task["poll"].(int); !ok || n <= 0 {
				missing = append(missing, "poll")
			}
			if n, ok := task["retries"].(int); !ok || n <= 0 {
				missing = append(missing, "retries")
			}
			if _, ok := task["until"]; !ok {
				missing = append(missing, "until")
			}
			if len(missing) > 0 {
				*out = append(*out, fmt.Sprintf("%s: %q lacks %s", file, task["name"], strings.Join(missing, ", ")))
			}
		}
	}
}

// A package mirror that stops answering mid-transfer once left an apt fetch
// method waiting forever, and the edge provision with it (28 min in
// node_tuning until the methods were killed by hand). Every role task that
// refreshes the apt cache or installs packages therefore runs under async, so
// Ansible kills the whole apt process group at the deadline, and retries, so
// a transient mirror stall heals while a dead one fails the task with an error.
func TestRolePackageTasksAreBoundedAndRetried(t *testing.T) {
	roles := "../../../ansible/collections/ansible_collections/frameworks/infra/roles"
	var unbounded []string
	checked := 0
	err := filepath.WalkDir(roles, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() && d.Name() == "molecule" {
			return filepath.SkipDir
		}
		if d.IsDir() || filepath.Base(filepath.Dir(path)) != "tasks" || !strings.HasSuffix(path, ".yml") {
			return nil
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		var tasks []map[string]any
		if err := yaml.Unmarshal(raw, &tasks); err != nil {
			return fmt.Errorf("%s: %w", path, err)
		}
		collectPackageTasks(tasks, strings.TrimPrefix(path, roles+"/"), &unbounded)
		if strings.Contains(string(raw), "ansible.builtin.apt:") || strings.Contains(string(raw), "ansible.builtin.package:") {
			checked++
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if checked == 0 {
		t.Fatal("found no package tasks; the roles path is wrong")
	}
	playbooks, err := filepath.Glob("../../../ansible/playbooks/*.yml")
	if err != nil || len(playbooks) == 0 {
		t.Fatalf("no playbooks found: %v", err)
	}
	for _, path := range playbooks {
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		var plays []map[string]any
		if err := yaml.Unmarshal(raw, &plays); err != nil {
			t.Fatalf("%s: %v", path, err)
		}
		for _, play := range plays {
			for _, section := range []string{"pre_tasks", "tasks", "post_tasks"} {
				items, _ := play[section].([]any)
				var tasks []map[string]any
				for _, item := range items {
					if m, ok := item.(map[string]any); ok {
						tasks = append(tasks, m)
					}
				}
				collectPackageTasks(tasks, filepath.Base(path), &unbounded)
			}
		}
	}
	if len(unbounded) > 0 {
		t.Fatalf("package tasks without a deadline and retries:\n%s", strings.Join(unbounded, "\n"))
	}
}

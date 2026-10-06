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

// vendorPackages records, for an external role our roles include, the package tasks its Debian
// install path runs at the version ansible/requirements.yml pins. Those tasks carry no deadline, so
// the including wrapper installs the same packages first under one; the vendor task then finds them
// present and never reaches a mirror.
type vendorPackages struct {
	requirement string   // ansible/requirements.yml role or collection name
	version     string   // pinned version the rest of the entry was read from
	include     string   // role name our wrapper includes
	tasksFrom   string   // include_role/import_role tasks_from, when the wrapper runs one vendor file
	wrapper     string   // our task file that includes the vendor role
	vendorFile  string   // vendor task file holding its package tasks, relative to the role
	vendorNames []string // the vendor package tasks' name arguments, in order
	preinstall  []string // what the wrapper's bounded install must name for those to be no-ops
}

var vendorPackageTasks = []vendorPackages{
	{
		requirement: "geerlingguy.postgresql", version: "3.5.2", include: "geerlingguy.postgresql",
		wrapper: "postgres/tasks/install.yml", vendorFile: "tasks/setup-Debian.yml",
		vendorNames: []string{"{{ postgresql_python_library }}", "{{ postgresql_packages }}"},
		// Every Debian/Ubuntu vars file of the pinned role sets postgresql_python_library to
		// python3-psycopg2; the wrapper sets postgresql_packages itself.
		preinstall: []string{"python3-psycopg2", "postgresql_packages"},
	},
	{
		requirement: "geerlingguy.redis", version: "1.9.1", include: "geerlingguy.redis",
		wrapper: "redis/tasks/install.yml", vendorFile: "tasks/setup-Debian.yml",
		vendorNames: []string{"{{ redis_package }}"},
		preinstall:  []string{"redis_package", "redis-server"},
	},
	{
		requirement: "prometheus.prometheus", version: "0.30.1", include: "prometheus.prometheus.prometheus",
		wrapper: "prometheus_stack/tasks/prometheus_community.yml", vendorFile: "../_common/tasks/preflight.yml",
		vendorNames: []string{"{{ _common_dependencies }}"},
		preinstall:  []string{"python3-apt"},
	},
	{
		requirement: "prometheus.prometheus", version: "0.30.1", include: "prometheus.prometheus.node_exporter",
		wrapper: "prometheus_stack/tasks/prometheus_community.yml", vendorFile: "../_common/tasks/preflight.yml",
		vendorNames: []string{"{{ _common_dependencies }}"},
		preinstall:  []string{"python3-apt"},
	},
	{
		// Only the config task file runs; it installs nothing.
		requirement: "idealista.clickhouse", version: "3.3.4", include: "idealista.clickhouse",
		tasksFrom: "config/clickhouse.yml", wrapper: "clickhouse/tasks/install.yml",
	},
}

func readTaskList(t *testing.T, path string) []map[string]any {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var tasks []map[string]any
	if err := yaml.Unmarshal(raw, &tasks); err != nil {
		t.Fatalf("%s: %v", path, err)
	}
	return tasks
}

func includedRole(task map[string]any) (name, tasksFrom string, ok bool) {
	for _, module := range []string{"ansible.builtin.include_role", "include_role", "ansible.builtin.import_role", "import_role"} {
		if args, found := task[module].(map[string]any); found {
			name, _ = args["name"].(string)
			tasksFrom, _ = args["tasks_from"].(string)
			return name, tasksFrom, true
		}
	}
	return "", "", false
}

// The vendor roles' own apt tasks have no deadline (TestRolePackageTasksAreBoundedAndRetried only
// sees our roles), so every external role a wrapper includes must find its packages already
// installed by a bounded wrapper task, or a stalled mirror hangs provision inside the vendor role.
func TestVendorRolePackagesArePreinstalledUnderBound(t *testing.T) {
	ansibleDir := "../../../ansible"
	roles := ansibleDir + "/collections/ansible_collections/frameworks/infra/roles"

	var requirements struct {
		Roles       []struct{ Name, Version string } `yaml:"roles"`
		Collections []struct{ Name, Version string } `yaml:"collections"`
	}
	raw, err := os.ReadFile(ansibleDir + "/requirements.yml")
	if err != nil {
		t.Fatal(err)
	}
	if err = yaml.Unmarshal(raw, &requirements); err != nil {
		t.Fatal(err)
	}
	pinned := map[string]string{}
	for _, r := range append(requirements.Roles, requirements.Collections...) {
		pinned[r.Name] = r.Version
	}

	known := map[string]vendorPackages{}
	for _, v := range vendorPackageTasks {
		if pinned[v.requirement] != v.version {
			t.Errorf("%s is pinned at %q but its package tasks were recorded at %s: re-read the vendor role and update vendorPackageTasks",
				v.requirement, pinned[v.requirement], v.version)
		}
		known[v.wrapper+"|"+v.include] = v
	}

	err = filepath.WalkDir(roles, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() && d.Name() == "molecule" {
			return filepath.SkipDir
		}
		if d.IsDir() || filepath.Base(filepath.Dir(path)) != "tasks" || !strings.HasSuffix(path, ".yml") {
			return nil
		}
		rel := strings.TrimPrefix(path, roles+"/")
		tasks := readTaskList(t, path)
		for i, task := range tasks {
			name, tasksFrom, ok := includedRole(task)
			if !ok || strings.HasPrefix(name, "frameworks.") || strings.Contains(name, "{{") {
				continue
			}
			v, recorded := known[rel+"|"+name]
			if !recorded {
				t.Errorf("%s includes external role %s, which vendorPackageTasks does not cover", rel, name)
				continue
			}
			if v.tasksFrom != tasksFrom {
				t.Errorf("%s includes %s tasks_from %q; vendorPackageTasks records %q", rel, name, tasksFrom, v.tasksFrom)
			}
			if len(v.preinstall) == 0 {
				continue
			}
			covered := false
			for _, earlier := range tasks[:i] {
				for module := range packageModules {
					args, isPkg := earlier[module].(map[string]any)
					if !isPkg || !mirrorTask(args) {
						continue
					}
					var unbounded []string
					collectPackageTasks([]map[string]any{earlier}, rel, &unbounded)
					names := fmt.Sprint(args["name"])
					all := len(unbounded) == 0
					for _, want := range v.preinstall {
						all = all && strings.Contains(names, want)
					}
					covered = covered || all
				}
			}
			if !covered {
				t.Errorf("%s includes %s without first installing %v under a deadline and retries", rel, name, v.preinstall)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}

	// With the vendor sources installed (make ansible-galaxy-install), check the recorded package tasks
	// against the vendor files themselves.
	for _, v := range vendorPackageTasks {
		if v.vendorFile == "" {
			continue
		}
		root := ansibleDir + "/.cache/roles/" + v.include
		if parts := strings.Split(v.include, "."); len(parts) == 3 {
			root = ansibleDir + "/.cache/collections/ansible_collections/" + parts[0] + "/" + parts[1] + "/roles/" + parts[2]
		}
		file := filepath.Join(root, v.vendorFile)
		if _, err := os.Stat(file); err != nil {
			t.Logf("%s not installed; checked the recorded package tasks against the requirements pin only", v.include)
			continue
		}
		var got []string
		for _, task := range readTaskList(t, file) {
			for module := range packageModules {
				if args, ok := task[module].(map[string]any); ok && mirrorTask(args) {
					got = append(got, fmt.Sprint(args["name"]))
				}
			}
		}
		if strings.Join(got, "\n") != strings.Join(v.vendorNames, "\n") {
			t.Errorf("%s package tasks in %s are %q; vendorPackageTasks records %q", v.include, v.vendorFile, got, v.vendorNames)
		}
	}
}

package provisioner

import (
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

type yugabyteRoleTask struct {
	Name   string `yaml:"name"`
	Listen any    `yaml:"listen"`
	Notify any    `yaml:"notify"`
	When   any    `yaml:"when"`
}

func yugabyteRoleTasks(t *testing.T, file string) []yugabyteRoleTask {
	t.Helper()
	var tasks []yugabyteRoleTask
	if err := yaml.Unmarshal([]byte(databaseRoleTaskFile(t, "yugabyte", file)), &tasks); err != nil {
		t.Fatalf("parse %s: %v", file, err)
	}
	return tasks
}

func yugabyteTaskWhen(task yugabyteRoleTask) string {
	switch when := task.When.(type) {
	case string:
		return when
	case []any:
		parts := make([]string, len(when))
		for i, part := range when {
			parts[i], _ = part.(string)
		}
		return strings.Join(parts, " AND ")
	}
	return ""
}

// TestYugabyteRestartsFollowTheUpgradeScope pins that every master and tserver restart, from the install and
// configure handlers and from the restart tag, honours yugabyte_restart_scope, so cluster upgrade can restart every
// master before any tserver.
func TestYugabyteRestartsFollowTheUpgradeScope(t *testing.T) {
	want := map[string]string{
		"yb-master":  "yugabyte_restart_scope in ['all', 'master']",
		"yb-tserver": "yugabyte_restart_scope in ['all', 'tserver']",
	}
	found := 0
	for _, file := range []string{"../handlers/main.yml", "restart.yml"} {
		for _, task := range yugabyteRoleTasks(t, file) {
			for process, condition := range want {
				if task.Name != "Restart "+process {
					continue
				}
				found++
				wantCondition := condition
				if file == "../handlers/main.yml" {
					wantCondition = "not ansible_check_mode AND " + condition
				}
				if yugabyteTaskWhen(task) != wantCondition {
					t.Errorf("%s: %q runs when %q, want %q", file, task.Name, yugabyteTaskWhen(task), wantCondition)
				}
			}
		}
	}
	if found != 4 {
		t.Fatalf("found %d master and tserver restarts, want the two handlers and the two restart-tag tasks", found)
	}
}

// TestYugabyteInstallRefusesEngineChangesOutsideUpgrade pins the guard that keeps provisioning from swapping the
// engine under a node that already joined the universe.
func TestYugabyteInstallRefusesEngineChangesOutsideUpgrade(t *testing.T) {
	for _, task := range yugabyteRoleTasks(t, "install.yml") {
		if task.Name != "Refuse an engine change outside cluster upgrade" {
			continue
		}
		when := yugabyteTaskWhen(task)
		for _, condition := range []string{
			"yugabyte_engine_guard_bootstrap.stat.exists",
			"yugabyte_current.stat.lnk_source != yugabyte_release_dir",
			"not yugabyte_in_place_identity.stat.exists",
			"not (yugabyte_allow_engine_change | bool)",
		} {
			if !strings.Contains(when, condition) {
				t.Fatalf("engine change guard runs when %q; it lacks %q", when, condition)
			}
		}
		return
	}
	t.Fatal("install.yml has no engine change guard")
}

func TestYugabyteInstallerPublishesOnlyCompleteSeparateArtifacts(t *testing.T) {
	text := databaseRoleTaskFile(t, "yugabyte", "install.yml")
	steps := []string{
		"name: Refuse an engine change outside cluster upgrade",
		"name: Refuse to overwrite an incomplete selected release",
		"name: Remove unpublished partial extraction",
		"name: Create clean artifact directory",
		"name: Extract YugabyteDB tarball into clean artifact directory",
		"name: Run YugabyteDB post_install.sh",
		"name: Record installation sentinel",
		"name: Select complete YugabyteDB release atomically",
	}
	last := -1
	for _, step := range steps {
		at := strings.Index(text, step)
		if at <= last {
			t.Fatalf("missing or out-of-order installer step: %s", step)
		}
		last = at
	}
	if strings.Contains(text, `dest: "{{ yugabyte_install_dir }}"`) || !strings.Contains(text, `dest: "{{ yugabyte_release_dir }}"`) {
		t.Fatal("archive must be extracted only into its separate artifact directory")
	}
	if !strings.Contains(text, `mv -Tf "$pending/current" "$link"`) {
		t.Fatal("selector must be atomically replaced")
	}
	vars := databaseRoleTaskFile(t, "yugabyte", "../vars/main.yml")
	if !strings.Contains(vars, `yugabyte_bin_dir: "{{ yugabyte_release_dir }}/bin"`) {
		t.Fatal("service units must use physical release paths")
	}
	restart := databaseRoleTaskFile(t, "yugabyte", "restart.yml")
	if strings.Index(restart, "name: Select tserver release") > strings.Index(restart, "name: Restart yb-tserver") {
		t.Fatal("tserver unit must be switched before its admitted restart")
	}
	for _, task := range yugabyteRoleTasks(t, "restart.yml") {
		if task.Name == "Select tserver release only in its admitted restart phase" && yugabyteTaskWhen(task) != "yugabyte_restart_scope == 'tserver'" {
			t.Fatal("a generic restart must not select a new engine from the release manifest")
		}
	}
}

func TestYugabyteMasterOnlyPhaseDoesNotStartOrWaitForTServer(t *testing.T) {
	for file, name := range map[string]string{
		"service.yml":   "Start yb-tserver",
		"restart.yml":   "Wait for YSQL after restart",
		"configure.yml": "Render yb-tserver systemd unit",
	} {
		found := false
		for _, task := range yugabyteRoleTasks(t, file) {
			if task.Name == name {
				found = true
				if got := yugabyteTaskWhen(task); got != "yugabyte_restart_scope in ['all', 'tserver']" {
					t.Fatalf("%s: %s can run during a master-only phase: %q", file, name, got)
				}
			}
		}
		if !found {
			t.Fatalf("missing task %s in %s", name, file)
		}
	}
}

func TestYugabyteAppliedConfigurationReceiptFollowsProcessStart(t *testing.T) {
	for _, file := range []string{"configure.yml", "install.yml"} {
		if strings.Contains(databaseRoleTaskFile(t, "yugabyte", file), "record-tserver-config.yml") {
			t.Fatalf("%s records desired configuration without starting the tserver", file)
		}
	}
	for file, before := range map[string]string{
		"restart.yml":          "name: Wait for YSQL after restart",
		"../handlers/main.yml": "name: Restart yb-tserver",
	} {
		source := databaseRoleTaskFile(t, "yugabyte", file)
		include := "import_tasks: record-tserver-config.yml"
		if file == "../handlers/main.yml" {
			include = "include_tasks: record-tserver-config.yml"
		}
		if !strings.Contains(source, before) || strings.Index(source, include) <= strings.Index(source, before) {
			t.Fatalf("%s does not record configuration after %s", file, before)
		}
	}
	for _, task := range yugabyteRoleTasks(t, "service.yml") {
		if task.Name == "Start yb-tserver" && task.Notify != "yugabyte record tserver config" {
			t.Fatal("only a changed start must notify the applied-configuration handler")
		}
	}
}

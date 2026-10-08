package provisioner

import (
	"regexp"
	"slices"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// mistserverDefaultPattern reads a regex the role keeps in its defaults.
// Python's re (Ansible's regex_findall) and Go's RE2 agree on these patterns.
func mistserverDefaultPattern(t *testing.T, name string) *regexp.Regexp {
	t.Helper()
	var defaults map[string]any
	if err := yaml.Unmarshal([]byte(readRepoFile(t, mistserverRoleDir+"defaults/main.yml")), &defaults); err != nil {
		t.Fatal(err)
	}
	pattern := stringValue(defaults[name])
	if pattern == "" {
		t.Fatalf("mistserver defaults have no %s", name)
	}
	return regexp.MustCompile(pattern)
}

// environmentChangeTask returns the task that decides whether a render changed
// the controller's start-time environment, after checking it compares with
// the named pattern and notifies the hard restart.
func environmentChangeTask(t *testing.T, file, name, pattern string) {
	t.Helper()
	task := roleTaskByName(t, mistserverRoleDir+file, name)
	if !strings.Contains(stringValue(task["changed_when"]), "regex_findall("+pattern+")") {
		t.Fatalf("%s %q does not compare with %s: %v", file, name, pattern, task["changed_when"])
	}
	if !slices.Equal(notifyTopics(task), []string{"mistserver restart"}) {
		t.Fatalf("%s %q notifies %v, want the hard restart", file, name, notifyTopics(task))
	}
}

func environmentChanged(re *regexp.Regexp, installed, rendered string) bool {
	return !slices.Equal(re.FindAllString(installed, -1), re.FindAllString(rendered, -1))
}

// A USR1 reload execs the new MistController with the running process's
// environ (mistserver src/controller/controller.cpp: execvp(myFile, argv)),
// not the unit's. A unit change confined to ExecStart and other directives
// waits for the next start; a changed Environment=/EnvironmentFile= line,
// such as the LD_LIBRARY_PATH the bundled ONNX libraries resolve through,
// restarts MistServer so a later binary swap does not run without it.
func TestMistserverUnitEnvironmentChangesRestart(t *testing.T) {
	render := roleTaskByName(t, mistserverRoleDir+"tasks/configure-linux.yml", "Render systemd unit")
	if !slices.Equal(notifyTopics(render), []string{"mistserver restart pending"}) {
		t.Fatalf("Render systemd unit notifies %v, want only the pending restart", notifyTopics(render))
	}
	environmentChangeTask(t, "tasks/configure-linux.yml", "Report MistServer unit environment change", "mistserver_unit_environment_pattern")
	re := mistserverDefaultPattern(t, "mistserver_unit_environment_pattern")

	unit := readRepoFile(t, mistserverRoleDir+"templates/mistserver.service.j2")
	execStart := regexp.MustCompile(`(?m)^ExecStart=.*$`)
	if !execStart.MatchString(unit) || !strings.Contains(unit, "\nEnvironment=LD_LIBRARY_PATH=") {
		t.Fatal("mistserver.service.j2 no longer has the ExecStart and LD_LIBRARY_PATH lines this test edits")
	}
	execOnly := execStart.ReplaceAllString(unit, "ExecStart=/opt/frameworks/mistserver/bin/MistController -c /etc/mistserver.conf -n")
	if environmentChanged(re, unit, execOnly) {
		t.Fatal("an ExecStart-only unit change classifies as an environment change; it must wait for the next start")
	}
	ldPath := strings.Replace(unit, "\nEnvironment=LD_LIBRARY_PATH=", "\nEnvironment=LD_LIBRARY_PATH=/opt/other/lib:", 1)
	if !environmentChanged(re, unit, ldPath) {
		t.Fatal("an LD_LIBRARY_PATH change does not classify as an environment change; it must restart")
	}
	envFile := strings.Replace(unit, "\nEnvironmentFile=", "\nEnvironmentFile=/etc/other.env\n#", 1)
	if !environmentChanged(re, unit, envFile) {
		t.Fatal("an EnvironmentFile change does not classify as an environment change")
	}
}

// The env file and the launchd EnvironmentVariables are the same start-time
// environment; their changes restart too, while the launchd wrapper and the
// rest of the plist wait for the next start.
func TestMistserverEnvFileAndPlistEnvironmentChangesRestart(t *testing.T) {
	environmentChangeTask(t, "tasks/configure-linux.yml", "Report MistServer environment change", "mistserver_env_assignment_pattern")
	environmentChangeTask(t, "tasks/configure-darwin.yml", "Report MistServer environment change", "mistserver_env_assignment_pattern")
	environmentChangeTask(t, "tasks/configure-darwin.yml", "Report MistServer launchd environment change", "mistserver_plist_environment_pattern")

	env := mistserverDefaultPattern(t, "mistserver_env_assignment_pattern")
	base := "# MistServer environment (managed by frameworks.infra.mistserver)\nMIST_DEBUG=3\nMIST_API_USERNAME=frameworks\nMIST_API_PASSWORD=a\n"
	if environmentChanged(env, base, strings.Replace(base, "# MistServer environment", "# MistServer env", 1)) {
		t.Fatal("a header comment change classifies as an environment change")
	}
	for _, changed := range []string{
		strings.Replace(base, "MIST_DEBUG=3", "MIST_DEBUG=4", 1),
		strings.Replace(base, "MIST_API_PASSWORD=a", "MIST_API_PASSWORD=b", 1),
		base + "LD_LIBRARY_PATH=/opt/other/lib\n",
	} {
		if !environmentChanged(env, base, changed) {
			t.Fatalf("env file change %q does not classify as an environment change", changed)
		}
	}

	plistRe := mistserverDefaultPattern(t, "mistserver_plist_environment_pattern")
	plist := readRepoFile(t, mistserverRoleDir+"templates/mistserver.plist.j2")
	withEnv := strings.Replace(plist, "    <key>RunAtLoad</key>", "    <key>EnvironmentVariables</key>\n    <dict>\n        <key>DYLD_LIBRARY_PATH</key>\n        <string>/opt/lib</string>\n    </dict>\n    <key>RunAtLoad</key>", 1)
	if withEnv == plist {
		t.Fatal("mistserver.plist.j2 no longer has the RunAtLoad key this test edits")
	}
	if !environmentChanged(plistRe, plist, withEnv) {
		t.Fatal("a launchd EnvironmentVariables change does not classify as an environment change")
	}
	if environmentChanged(plistRe, plist, strings.Replace(plist, "<true/>", "<false/>", 1)) {
		t.Fatal("a non-environment plist change classifies as an environment change")
	}
	for _, name := range []string{"Render launchd wrapper script", "Render launchd plist"} {
		if task := roleTaskByName(t, mistserverRoleDir+"tasks/configure-darwin.yml", name); !slices.Equal(notifyTopics(task), []string{"mistserver restart pending"}) {
			t.Fatalf("%s notifies %v, want only the pending restart", name, notifyTopics(task))
		}
	}
}

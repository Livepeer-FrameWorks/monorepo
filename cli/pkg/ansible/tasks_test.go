package ansible

import (
	"testing"
)

func TestTaskGetURL_shape(t *testing.T) {
	t.Parallel()
	task := TaskGetURL("https://example.com/x.tgz", "/tmp/x.tgz", "sha256:abc")
	if task.Module != "ansible.builtin.get_url" {
		t.Errorf("wrong module: %q", task.Module)
	}
	if task.Args["url"] != "https://example.com/x.tgz" {
		t.Errorf("missing url: %v", task.Args["url"])
	}
	if task.Args["dest"] != "/tmp/x.tgz" {
		t.Errorf("missing dest: %v", task.Args["dest"])
	}
	if task.Args["checksum"] != "sha256:abc" {
		t.Errorf("missing checksum: %v", task.Args["checksum"])
	}
}

func TestTaskGetURL_omitsChecksumWhenEmpty(t *testing.T) {
	t.Parallel()
	task := TaskGetURL("https://example.com/x.tgz", "/tmp/x.tgz", "")
	if _, ok := task.Args["checksum"]; ok {
		t.Error("checksum key must be absent when caller passes empty string")
	}
}

func TestTaskUnarchive_stripComponents(t *testing.T) {
	t.Parallel()
	task := TaskUnarchive("/tmp/kafka.tgz", "/opt/kafka", "/opt/kafka/bin/kafka-server-start.sh", UnarchiveOpts{StripComponents: 1})
	if task.Module != "ansible.builtin.unarchive" {
		t.Errorf("wrong module: %q", task.Module)
	}
	if task.Args["remote_src"] != true {
		t.Error("remote_src must be true (archive already on host)")
	}
	opts, ok := task.Args["extra_opts"].([]string)
	if !ok || len(opts) != 1 || opts[0] != "--strip-components=1" {
		t.Errorf("extra_opts should contain --strip-components=1; got %v", task.Args["extra_opts"])
	}
}

func TestTaskUnarchive_noStripComponents(t *testing.T) {
	t.Parallel()
	task := TaskUnarchive("/tmp/x.tgz", "/opt/x", "/opt/x/sentinel", UnarchiveOpts{})
	if _, ok := task.Args["extra_opts"]; ok {
		t.Error("extra_opts must be absent when StripComponents is 0")
	}
}

func TestTaskUnarchive_panicsOnEmptyCreates(t *testing.T) {
	t.Parallel()
	defer func() {
		if r := recover(); r == nil {
			t.Error("TaskUnarchive must panic when creates is empty")
		}
	}()
	TaskUnarchive("/tmp/x.tgz", "/opt/x", "", UnarchiveOpts{})
}

func TestArtifactSentinel_rotatesOnIdentityChange(t *testing.T) {
	t.Parallel()
	a := ArtifactSentinel("/opt/kafka", "sha256:aa"+"https://x/kafka-4.2.0.tgz")
	b := ArtifactSentinel("/opt/kafka", "sha256:bb"+"https://x/kafka-4.3.0.tgz")
	if a == b {
		t.Errorf("sentinel must rotate when the identity key changes: got %q for both", a)
	}
}

func TestArtifactSentinel_stableForSameInput(t *testing.T) {
	t.Parallel()
	a := ArtifactSentinel("/opt/kafka", "sha256:aa"+"https://x/kafka-4.2.0.tgz")
	b := ArtifactSentinel("/opt/kafka", "sha256:aa"+"https://x/kafka-4.2.0.tgz")
	if a != b {
		t.Errorf("sentinel must be stable for identical input: %q vs %q", a, b)
	}
}

func TestArtifactSentinel_isUnderDest(t *testing.T) {
	t.Parallel()
	s := ArtifactSentinel("/opt/kafka", "any-key")
	if got := s[:len("/opt/kafka/.installed-")]; got != "/opt/kafka/.installed-" {
		t.Errorf("sentinel must live under dest with .installed- prefix; got %q", s)
	}
}

func TestTaskUnarchive_ownerAndGroupPropagate(t *testing.T) {
	t.Parallel()
	task := TaskUnarchive("/tmp/x.tgz", "/opt/x", "/opt/x/sentinel", UnarchiveOpts{
		Owner: "kafka", Group: "kafka",
	})
	if task.Args["owner"] != "kafka" || task.Args["group"] != "kafka" {
		t.Errorf("owner/group must land in Args: got owner=%v group=%v", task.Args["owner"], task.Args["group"])
	}
}

func TestTaskCopy_inlineContent(t *testing.T) {
	t.Parallel()
	task := TaskCopy("/etc/app.conf", "key=value\n", CopyOpts{
		Owner: "root",
		Group: "root",
		Mode:  "0644",
	})
	if task.Module != "ansible.builtin.copy" {
		t.Errorf("wrong module: %q", task.Module)
	}
	if task.Args["content"] != "key=value\n" {
		t.Errorf("content not inlined")
	}
	if task.Args["owner"] != "root" || task.Args["group"] != "root" || task.Args["mode"] != "0644" {
		t.Errorf("owner/group/mode not propagated: %+v", task.Args)
	}
}

func TestTaskPackage_defaultsToPresent(t *testing.T) {
	t.Parallel()
	task := TaskPackage("chrony", "")
	if task.Args["state"] != "present" {
		t.Errorf("empty state must default to present; got %v", task.Args["state"])
	}
}

func TestTaskPackage_respectsState(t *testing.T) {
	t.Parallel()
	task := TaskPackage("chrony", PackageAbsent)
	if task.Args["state"] != "absent" {
		t.Errorf("state not propagated; got %v", task.Args["state"])
	}
}

func TestTaskSystemdService_enabledPointer(t *testing.T) {
	t.Parallel()
	task := TaskSystemdService("foo", SystemdOpts{
		State:        "started",
		Enabled:      BoolPtr(true),
		DaemonReload: true,
	})
	if task.Module != "ansible.builtin.systemd_service" {
		t.Errorf("wrong module: %q", task.Module)
	}
	if task.Args["enabled"] != true {
		t.Errorf("enabled not propagated")
	}
	if task.Args["daemon_reload"] != true {
		t.Errorf("daemon_reload not propagated")
	}
	if task.Args["state"] != "started" {
		t.Errorf("state not propagated")
	}
}

func TestTaskSystemdService_nilEnabledOmitsKey(t *testing.T) {
	t.Parallel()
	task := TaskSystemdService("foo", SystemdOpts{State: "started"})
	if _, ok := task.Args["enabled"]; ok {
		t.Error("enabled key must be absent when Enabled is nil")
	}
}

func TestTaskWaitForPort_defaultsToStarted(t *testing.T) {
	t.Parallel()
	task := TaskWaitForPort(9093, WaitForOpts{})
	if task.Module != "ansible.builtin.wait_for" {
		t.Fatalf("wrong module: %q", task.Module)
	}
	if task.Args["port"] != 9093 {
		t.Fatalf("port not propagated: %v", task.Args["port"])
	}
	if task.Args["state"] != "started" {
		t.Fatalf("state must default to started; got %v", task.Args["state"])
	}
	if _, ok := task.Args["host"]; ok {
		t.Fatal("host key must be absent when Host is empty")
	}
}

func TestTaskWaitForPort_optionalArgsPropagate(t *testing.T) {
	t.Parallel()
	task := TaskWaitForPort(8123, WaitForOpts{
		Host:    "0.0.0.0",
		Delay:   2,
		Timeout: 30,
		Sleep:   1,
		When:    "service_ready | default(true)",
	})
	if task.Args["host"] != "0.0.0.0" {
		t.Fatalf("host not propagated: %v", task.Args["host"])
	}
	if task.Args["delay"] != 2 || task.Args["timeout"] != 30 || task.Args["sleep"] != 1 {
		t.Fatalf("delay/timeout/sleep not propagated: %+v", task.Args)
	}
	if task.When != "service_ready | default(true)" {
		t.Fatalf("when not propagated: %q", task.When)
	}
}

func TestTaskShell_panicsWithoutGuard(t *testing.T) {
	t.Parallel()
	defer func() {
		if r := recover(); r == nil {
			t.Error("TaskShell with no Creates/Removes/When must panic")
		}
	}()
	_ = TaskShell("echo hi", ShellOpts{})
}

func TestTaskShell_acceptsCreates(t *testing.T) {
	t.Parallel()
	task := TaskShell("make install", ShellOpts{
		Creates: "/opt/app/bin/app",
		Chdir:   "/tmp/app-src",
	})
	if task.Module != "ansible.builtin.shell" {
		t.Errorf("wrong module: %q", task.Module)
	}
	if task.Args["creates"] != "/opt/app/bin/app" {
		t.Error("creates not propagated")
	}
	if task.Args["chdir"] != "/tmp/app-src" {
		t.Error("chdir not propagated")
	}
}

func TestTaskShell_acceptsWhen(t *testing.T) {
	t.Parallel()
	task := TaskShell("echo test", ShellOpts{When: "ansible_facts.os_family == 'Archlinux'"})
	if task.When != "ansible_facts.os_family == 'Archlinux'" {
		t.Error("When predicate must propagate to Task.When")
	}
}

func TestTaskShell_environmentPropagates(t *testing.T) {
	t.Parallel()
	task := TaskShell("run app", ShellOpts{
		Creates:     "/tmp/done",
		Environment: map[string]string{"FOO": "bar"},
	})
	// environment: is a task-level attribute in Ansible, not a module arg, so
	// it must land on Task.Environment. If it leaks into Args it serializes
	// under the shell: module key and Ansible silently ignores it.
	if task.Environment["FOO"] != "bar" {
		t.Errorf("environment not propagated to Task.Environment: %v", task.Environment)
	}
	if _, bad := task.Args["environment"]; bad {
		t.Error("environment must not be emitted as a shell-module arg")
	}
}

func TestTaskShell_extraMergesThrough(t *testing.T) {
	t.Parallel()
	task := TaskShell("do thing", ShellOpts{
		Creates: "/tmp/done",
		Extra:   map[string]any{"executable": "/bin/zsh"},
	})
	if task.Args["executable"] != "/bin/zsh" {
		t.Error("Extra keys must merge into Args")
	}
}

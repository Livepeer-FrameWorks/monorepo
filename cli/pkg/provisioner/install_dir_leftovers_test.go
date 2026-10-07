package provisioner

import (
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func roleTaskByName(t *testing.T, path, name string) map[string]any {
	t.Helper()
	var tasks []any
	if err := yaml.Unmarshal([]byte(readRepoFile(t, path)), &tasks); err != nil {
		t.Fatalf("parse %s: %v", path, err)
	}
	var found map[string]any
	walkRoleBlocks(tasks, nil, func(task map[string]any, _ []map[string]any) {
		if task["name"] == name {
			found = task
		}
	})
	if found == nil {
		t.Fatalf("%s has no task %q", path, name)
	}
	return found
}

// The installed binary is the archive's binary renamed, not a second copy of
// it, and sentinels of earlier pins (dotfiles, which find skips unless hidden
// is set) are removed.
func TestNativeInstallsLeaveOneBinaryAndOneSentinel(t *testing.T) {
	const roles = "ansible/collections/ansible_collections/frameworks/infra/roles/"
	for _, tc := range []struct{ file, normalize, binary string }{
		{roles + "go_service/tasks/install.yml", "Normalize extracted binary name", `"{{ go_service_install_dir }}/{{ go_service_name }}"`},
		{roles + "privateer/tasks/install.yml", "Normalize Privateer binary name", `"{{ privateer_bin }}"`},
	} {
		task := roleTaskByName(t, tc.file, tc.normalize)
		script, ok := task["ansible.builtin.shell"].(string)
		if !ok {
			t.Fatalf("%s: %q is not a shell task", tc.file, tc.normalize)
		}
		if strings.Contains(script, `cp "$candidate"`) || !strings.Contains(script, `mv "$candidate" `+tc.binary) {
			t.Errorf("%s: %q must move the extracted binary into place, not copy it:\n%s", tc.file, tc.normalize, script)
		}
	}

	for _, tc := range []struct{ file, find, remove string }{
		{roles + "go_service/tasks/install.yml", "Find obsolete install sentinels", "Remove obsolete install sentinels"},
		{roles + "privateer/tasks/install.yml", "Find obsolete Privateer install sentinels", "Remove obsolete Privateer install sentinels"},
	} {
		spec, _ := moduleSpec(roleTaskByName(t, tc.file, tc.find), "find")
		if spec["hidden"] != true || spec["patterns"] != ".installed-*" {
			t.Errorf("%s: %q must set hidden: true to match .installed-* files: %v", tc.file, tc.find, spec)
		}
		remove, _ := moduleSpec(roleTaskByName(t, tc.file, tc.remove), "file")
		if remove["state"] != "absent" {
			t.Errorf("%s: %q must remove the found sentinels: %v", tc.file, tc.remove, remove)
		}
	}
}

// Linux Helmsman downloads its artifact once per pin: the sentinel names the
// pin, and a receipt of the installed binary's sha256 catches a binary the
// component updater replaced, so an apply with nothing to change downloads
// nothing and reports nothing changed.
func TestHelmsmanDownloadsOncePerPin(t *testing.T) {
	const file = "ansible/collections/ansible_collections/frameworks/infra/roles/helmsman/tasks/install-linux.yml"
	var tasks []any
	if err := yaml.Unmarshal([]byte(readRepoFile(t, file)), &tasks); err != nil {
		t.Fatal(err)
	}
	var download []map[string]any
	walkRoleBlocks(tasks, nil, func(task map[string]any, enclosing []map[string]any) {
		if _, ok := moduleSpec(task, "tempfile"); ok {
			t.Errorf("%s: %q extracts the artifact on every apply to verify it", file, task["name"])
		}
		if _, ok := moduleSpec(task, "get_url"); ok {
			download = enclosing
		}
	})
	gated := false
	for _, block := range download {
		conds, _ := block["when"].([]any)
		for _, cond := range conds {
			if cond == "helmsman_reinstall_required | bool" {
				gated = true
			}
		}
	}
	if !gated {
		t.Errorf("%s: the download must be gated on helmsman_reinstall_required", file)
	}
	decide := roleTaskByName(t, file, "Decide whether Helmsman reinstall is required")
	spec, _ := moduleSpec(decide, "set_fact")
	rule := stringValue(spec["helmsman_reinstall_required"])
	for _, want := range []string{"helmsman_sentinel_stat.stat.exists", "helmsman_binary_receipt_check.rc"} {
		if !strings.Contains(rule, want) {
			t.Errorf("reinstall decision must use %s: %s", want, rule)
		}
	}
	normalize := stringValue(roleTaskByName(t, file, "Normalize extracted binary name")["ansible.builtin.shell"])
	if !strings.Contains(normalize, `> "{{ helmsman_binary_receipt }}"`) {
		t.Errorf("normalize must record the installed binary's sha256 receipt:\n%s", normalize)
	}
}

// The native Linux install narrows the edge PKI and certificate directories to
// 0750 and 2770; the shared prepare step that creates them must not reset them
// to 0755 on every run.
func TestEdgePrepareLeavesTLSDirectoryModesToTheInstall(t *testing.T) {
	const file = "ansible/collections/ansible_collections/frameworks/infra/roles/edge/tasks/prepare.yml"
	var tasks []any
	if err := yaml.Unmarshal([]byte(readRepoFile(t, file)), &tasks); err != nil {
		t.Fatal(err)
	}
	narrowed := map[string]bool{"{{ edge_conf_dir }}/pki": true, "{{ edge_cert_dir }}": true}
	seen := 0
	walkRoleBlocks(tasks, nil, func(task map[string]any, _ []map[string]any) {
		spec, ok := moduleSpec(task, "file")
		if !ok || spec["state"] != "directory" {
			return
		}
		paths := []string{stringValue(spec["path"])}
		if loop, ok := task["loop"].([]any); ok {
			paths = paths[:0]
			for _, item := range loop {
				paths = append(paths, stringValue(item))
			}
		}
		for _, path := range paths {
			if !narrowed[path] {
				continue
			}
			seen++
			if _, set := spec["mode"]; set {
				t.Errorf("%s: %q sets a mode on %s, which the native install narrows", file, task["name"], path)
			}
		}
	})
	if seen != 2 {
		t.Fatalf("%s must still create both TLS directories; found %d", file, seen)
	}
}

// A carried artifact keeps the URL and checksum of the release that built it,
// while the desired version label names a later release; the binary keeps
// reporting the version it was built as. The reinstall decision therefore
// compares artifact identity (the sentinel keyed on checksum and URL, and the
// installed binary's receipt), never the version label, or every release
// apply reinstalls and restarts every carried service.
func TestReinstallDecisionsUseArtifactIdentityNotVersionLabels(t *testing.T) {
	const roles = "ansible/collections/ansible_collections/frameworks/infra/roles/"
	for _, tc := range []struct{ file, task, fact, sentinel string }{
		{roles + "go_service/tasks/install.yml", "Decide whether service binary reinstall is required", "go_service_reinstall_required", "go_service_install_sentinel_stat"},
		{roles + "privateer/tasks/install.yml", "Decide whether Privateer reinstall is required", "privateer_reinstall_required", "privateer_install_sentinel_stat"},
	} {
		spec, _ := moduleSpec(roleTaskByName(t, tc.file, tc.task), "set_fact")
		rule := stringValue(spec[tc.fact])
		if !strings.Contains(rule, tc.sentinel) {
			t.Errorf("%s: %s must use the artifact sentinel: %s", tc.file, tc.fact, rule)
		}
		if strings.Contains(rule, "_version") {
			t.Errorf("%s: %s compares a version label, which differs for a carried artifact: %s", tc.file, tc.fact, rule)
		}
	}
}

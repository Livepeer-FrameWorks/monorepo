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

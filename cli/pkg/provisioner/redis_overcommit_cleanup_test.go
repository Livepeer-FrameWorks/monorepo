package provisioner

import (
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// The redis role writes /etc/sysctl.d/92-frameworks-redis.conf on every host
// it installs a server on, and its cleanup left that drop-in behind after the
// last server was removed, so the host kept vm.overcommit_memory=1 forever.
// Cleanup now releases it once no Redis/Valkey server remains.
func TestRedisCleanupReleasesOvercommitDropIn(t *testing.T) {
	tasks := playbookTasksUnderTags(t, "playbooks/redis.yml", []string{"cleanup"}, map[string]any{"redis_instance": "foghorn"})
	const task = "Redis | release memory overcommit once no server remains"
	if !slices.Contains(tasks, task) {
		t.Fatalf("cleanup tag does not run %q; tasks run: %q", task, tasks)
	}
	cleanup := readRepoFile(t, testRolesDir+"redis/tasks/cleanup.yml")
	release := strings.Index(cleanup, "Redis | release memory overcommit once no server remains")
	unitRemoval := strings.Index(cleanup, "Remove named Redis systemd unit")
	if release < unitRemoval {
		t.Fatal("overcommit release must run after the instance's unit is removed, or the unit itself keeps the drop-in")
	}
}

func TestRedisReleaseOvercommitScript(t *testing.T) {
	script, err := filepath.Abs("../../../" + testRolesDir + "redis/files/redis-release-overcommit.sh")
	if err != nil {
		t.Fatal(err)
	}
	const dropIn = "etc/sysctl.d/92-frameworks-redis.conf"
	cases := []struct {
		name       string
		files      []string
		want       string
		wantDropIn bool
		skipDropIn bool
	}{
		{name: "last named server removed", files: nil, want: "removed"},
		{name: "only sentinel and exporter units remain", files: []string{
			"etc/systemd/system/frameworks-redis-foghorn-sentinel.service",
			"etc/systemd/system/frameworks-redis-other-exporter.service",
		}, want: "removed"},
		{name: "another named server remains", files: []string{
			"etc/systemd/system/frameworks-redis-other.service",
		}, want: "kept", wantDropIn: true},
		{name: "default package server enabled", files: []string{
			"etc/systemd/system/multi-user.target.wants/valkey-server.service",
		}, want: "kept", wantDropIn: true},
		{name: "no drop-in", skipDropIn: true, want: "absent"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			files := tc.files
			if !tc.skipDropIn {
				files = append(slices.Clone(files), dropIn)
			}
			for _, rel := range files {
				path := filepath.Join(root, rel)
				if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, []byte("vm.overcommit_memory=1\n"), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			out, err := exec.CommandContext(t.Context(), "sh", script, root).CombinedOutput()
			if err != nil {
				t.Fatalf("script: %v\n%s", err, out)
			}
			if got := strings.TrimSpace(string(out)); got != tc.want {
				t.Fatalf("script printed %q, want %q", got, tc.want)
			}
			_, statErr := os.Stat(filepath.Join(root, dropIn))
			if exists := statErr == nil; exists != tc.wantDropIn {
				t.Fatalf("drop-in exists = %v, want %v", exists, tc.wantDropIn)
			}
		})
	}
}

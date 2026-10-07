package provisioner

import (
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

type yugabytePruneTree struct {
	root, install, proc string
}

// newYugabytePruneTree lays out an install dir holding the in-place tree from
// before release directories, the selected release and an older release.
func newYugabytePruneTree(t *testing.T) yugabytePruneTree {
	t.Helper()
	root := t.TempDir()
	tree := yugabytePruneTree{root: root, install: filepath.Join(root, "yugabyte"), proc: filepath.Join(root, "proc")}
	files := []string{
		"conf/master.conf",
		"bin/yb-server", "lib/libyb.so", "postgres/bin/postgres", ".post_install_done", ".installed-legacyid",
		"releases/new/bin/yb-server", "releases/new/lib/libyb.so", "releases/new/postgres/bin/postgres",
		"releases/new/.installed", "releases/new/.post_install_done",
		"releases/old/bin/yb-server", "releases/old/postgres/bin/postgres",
	}
	for _, file := range files {
		path := filepath.Join(tree.install, file)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, nil, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink(filepath.Join(tree.install, "releases", "new"), filepath.Join(tree.install, "current")); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(tree.proc, 0o755); err != nil {
		t.Fatal(err)
	}
	return tree
}

// running records a process executing path, relative to the install dir.
func (tree yugabytePruneTree) running(t *testing.T, pid, path string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(tree.proc, pid), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(tree.install, path), filepath.Join(tree.proc, pid, "exe")); err != nil {
		t.Fatal(err)
	}
}

func (tree yugabytePruneTree) prune(t *testing.T, mode string, units ...string) []string {
	t.Helper()
	script := ansibleTreePath(t, "collections", "ansible_collections", "frameworks", "infra", "roles", "yugabyte", "files", "yugabyte-prune-releases.sh")
	cmd := exec.CommandContext(t.Context(), "sh", append([]string{script, tree.install, mode}, units...)...)
	cmd.Env = append(os.Environ(), "PROC_ROOT="+tree.proc)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("yugabyte-prune-releases.sh %s: %v\n%s", mode, err, out)
	}
	return strings.Split(strings.TrimSpace(string(out)), "\n")
}

func (tree yugabytePruneTree) remaining(t *testing.T) []string {
	t.Helper()
	entries, err := os.ReadDir(tree.install)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, entry := range entries {
		names = append(names, entry.Name())
	}
	releases, err := os.ReadDir(filepath.Join(tree.install, "releases"))
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range releases {
		names = append(names, "releases/"+entry.Name())
	}
	return names
}

func TestYugabytePruneRemovesTreesNoProcessRuns(t *testing.T) {
	tree := newYugabytePruneTree(t)
	tree.running(t, "100", "releases/new/bin/yb-server")
	tree.running(t, "101", "releases/new/postgres/bin/postgres")

	check := tree.prune(t, "check")
	want := []string{"bin", "lib", "postgres", ".post_install_done", ".installed-legacyid", "releases/old"}
	if len(check) != len(want) {
		t.Fatalf("check reported %v, want %d removals of %v", check, len(want), want)
	}
	for _, line := range check {
		if !strings.HasPrefix(line, "would remove ") {
			t.Fatalf("check mode must only report: %q", line)
		}
	}
	if got := tree.remaining(t); len(got) != 10 {
		t.Fatalf("check mode removed files: %v", got)
	}

	tree.prune(t, "apply")
	got := tree.remaining(t)
	slices.Sort(got)
	if wantLeft := []string{"conf", "current", "releases", "releases/new"}; !slices.Equal(got, wantLeft) {
		t.Fatalf("after apply %v remain, want %v", got, wantLeft)
	}
	if _, err := os.Stat(filepath.Join(tree.install, "conf", "master.conf")); err != nil {
		t.Fatalf("live configuration removed: %v", err)
	}
}

// A tserver still running the in-place binary or an older release keeps that
// tree until it restarts onto the selected release.
func TestYugabytePruneKeepsTreesARunningProcessUses(t *testing.T) {
	tree := newYugabytePruneTree(t)
	tree.running(t, "100", "bin/yb-server")
	tree.running(t, "101", "releases/old/postgres/bin/postgres")
	if out := tree.prune(t, "apply"); len(out) != 1 || out[0] != "" {
		t.Fatalf("pruned trees in use: %v", out)
	}
	if got := tree.remaining(t); len(got) != 10 {
		t.Fatalf("trees in use were removed: %v", got)
	}
}

// A process that runs from elsewhere but maps a library of the in-place tree
// keeps that tree.
func TestYugabytePruneKeepsTreesAProcessMaps(t *testing.T) {
	tree := newYugabytePruneTree(t)
	tree.running(t, "100", "releases/new/bin/yb-server")
	install, err := filepath.EvalSymlinks(tree.install)
	if err != nil {
		t.Fatal(err)
	}
	maps := "7f0000000000-7f0000001000 r-xp 00000000 08:01 42 " + filepath.Join(install, "lib", "libyb.so") + "\n"
	if err := os.WriteFile(filepath.Join(tree.proc, "100", "maps"), []byte(maps), 0o644); err != nil {
		t.Fatal(err)
	}
	tree.prune(t, "apply")
	got := tree.remaining(t)
	if !slices.Contains(got, "lib") || !slices.Contains(got, "bin") {
		t.Fatalf("in-place tree removed while a process maps it: %v", got)
	}
	if slices.Contains(got, "releases/old") {
		t.Fatalf("unused release kept: %v", got)
	}
}

// A stopped tserver's unit keeps naming its release until its admitted
// restart switches it; that release must survive so the unit can start.
func TestYugabytePruneKeepsTreesAUnitNames(t *testing.T) {
	tree := newYugabytePruneTree(t)
	unit := filepath.Join(tree.root, "yb-tserver.service")
	content := "[Service]\nExecStart=" + filepath.Join(tree.install, "releases", "old", "bin", "yb-server") +
		" --flagfile " + filepath.Join(tree.install, "conf", "tserver.conf") + "\n"
	if err := os.WriteFile(unit, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	tree.prune(t, "apply", unit, filepath.Join(tree.root, "missing.service"))
	got := tree.remaining(t)
	if !slices.Contains(got, "releases/old") {
		t.Fatalf("release a unit names was removed: %v", got)
	}
	if slices.Contains(got, "bin") {
		t.Fatalf("the unit's conf path must not keep the in-place tree: %v", got)
	}
}

func TestYugabytePruneNeedsASelectedRelease(t *testing.T) {
	tree := newYugabytePruneTree(t)
	if err := os.Remove(filepath.Join(tree.install, "current")); err != nil {
		t.Fatal(err)
	}
	if out := tree.prune(t, "apply"); len(out) != 1 || out[0] != "" {
		t.Fatalf("pruned without a selected release: %v", out)
	}
}

// The role prunes after selecting the release, reports what it would remove
// in check mode, and never prunes from the restart path.
func TestYugabyteRoleRunsReleasePruneAfterSelection(t *testing.T) {
	install := readRepoFile(t, "ansible/collections/ansible_collections/frameworks/infra/roles/yugabyte/tasks/install.yml")
	selectAt := strings.Index(install, "name: Select complete YugabyteDB release atomically")
	pruneAt := strings.Index(install, "name: Remove YugabyteDB engine trees no process runs")
	if selectAt < 0 || pruneAt < selectAt {
		t.Fatalf("prune must follow release selection (select %d, prune %d)", selectAt, pruneAt)
	}
	task := roleTaskByName(t, "ansible/collections/ansible_collections/frameworks/infra/roles/yugabyte/tasks/install.yml",
		"Remove YugabyteDB engine trees no process runs")
	spec, ok := moduleSpec(task, "script")
	if !ok || !strings.Contains(stringValue(spec["cmd"]), "yugabyte-prune-releases.sh") ||
		!strings.Contains(stringValue(spec["cmd"]), "'check' if ansible_check_mode else 'apply'") {
		t.Fatalf("prune task must run the script in check or apply mode: %v", task)
	}
	if task["check_mode"] != false {
		t.Fatalf("prune must run in check mode to report removals: %v", task)
	}
	if task["changed_when"] != "yugabyte_release_prune.stdout | default('') | trim | length > 0" {
		t.Fatalf("prune must report a change for every removed or removable tree: %v", task["changed_when"])
	}
	for _, unit := range []string{"yugabyte_master_unit_path", "yugabyte_tserver_unit_path"} {
		if !strings.Contains(stringValue(spec["cmd"]), unit) {
			t.Fatalf("prune must keep the trees %s names: %v", unit, spec["cmd"])
		}
	}
}

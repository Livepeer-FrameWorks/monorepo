package updater

import (
	"os"
	"path/filepath"
	"testing"
)

// The existing replaceDirsAtomically tests exercise the rollback paths; these
// cover the success paths the swap is actually for.

// Fresh install: the destination does not exist yet, so the staged dir is
// renamed into place and after() commits.
func TestReplaceDirsAtomicallyFreshInstallSuccess(t *testing.T) {
	parent := t.TempDir()
	staged := filepath.Join(parent, "staged")
	dst := filepath.Join(parent, "mistserver")
	if err := os.MkdirAll(filepath.Join(staged, "bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(staged, "bin", "MistController"), []byte("new"), 0o755); err != nil {
		t.Fatal(err)
	}

	afterRan := false
	err := replaceDirsAtomically([]dirReplacement{{src: staged, dst: dst}}, func() error {
		afterRan = true
		return nil
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !afterRan {
		t.Fatal("after() should run on success")
	}
	got, err := os.ReadFile(filepath.Join(dst, "bin", "MistController"))
	if err != nil || string(got) != "new" {
		t.Fatalf("destination not installed: content=%q err=%v", got, err)
	}
	// The staged dir was renamed into place, so it no longer exists.
	if _, err := os.Stat(staged); !os.IsNotExist(err) {
		t.Fatalf("staged dir should have been renamed away, stat err=%v", err)
	}
}

// Existing destination, successful commit: the dirs are exchanged and the old
// install (now sitting at src) is removed.
func TestReplaceDirsAtomicallyExistingDstSuccessRemovesOldInstall(t *testing.T) {
	parent := t.TempDir()
	staged := filepath.Join(parent, "staged")
	dst := filepath.Join(parent, "mistserver")
	for _, d := range []string{filepath.Join(staged, "bin"), filepath.Join(dst, "bin")} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(dst, "bin", "MistController"), []byte("old"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(staged, "bin", "MistController"), []byte("new"), 0o755); err != nil {
		t.Fatal(err)
	}

	err := replaceDirsAtomically([]dirReplacement{{src: staged, dst: dst}}, func() error { return nil })
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	got, err := os.ReadFile(filepath.Join(dst, "bin", "MistController"))
	if err != nil || string(got) != "new" {
		t.Fatalf("destination not updated: content=%q err=%v", got, err)
	}
	// After a successful exchange the previous install (now at src) is cleaned.
	if _, err := os.Stat(staged); !os.IsNotExist(err) {
		t.Fatalf("old install at src should be removed, stat err=%v", err)
	}
}

// copyDirTree must reproduce directories, regular files (with mode), and
// symlinks verbatim.
func TestCopyDirTreeFilesDirsAndSymlinks(t *testing.T) {
	root := t.TempDir()
	src := filepath.Join(root, "src")
	dst := filepath.Join(root, "dst")
	if err := os.MkdirAll(filepath.Join(src, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(src, "sub", "file.txt"), []byte("payload"), 0o640); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("sub/file.txt", filepath.Join(src, "link")); err != nil {
		t.Fatal(err)
	}

	if err := copyDirTree(src, dst); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	copied := filepath.Join(dst, "sub", "file.txt")
	got, err := os.ReadFile(copied)
	if err != nil || string(got) != "payload" {
		t.Fatalf("regular file not copied: content=%q err=%v", got, err)
	}
	info, err := os.Stat(copied)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o640 {
		t.Fatalf("file mode not preserved: %v", info.Mode().Perm())
	}
	target, err := os.Readlink(filepath.Join(dst, "link"))
	if err != nil {
		t.Fatalf("symlink not copied: %v", err)
	}
	if target != "sub/file.txt" {
		t.Fatalf("symlink target = %q, want sub/file.txt", target)
	}
}

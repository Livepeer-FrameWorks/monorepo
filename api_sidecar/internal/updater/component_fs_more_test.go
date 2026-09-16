package updater

import (
	"os"
	"path/filepath"
	"testing"
)

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

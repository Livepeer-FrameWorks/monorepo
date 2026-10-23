package dashcheck

import (
	"os"
	"path/filepath"
	"sort"
	"testing"
)

func writeSourceFile(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("package x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestWalkRepoGoSourcesSkipsTempDirInsideCheckout(t *testing.T) {
	root := t.TempDir()
	writeSourceFile(t, filepath.Join(root, "svc", "metrics.go"))
	writeSourceFile(t, filepath.Join(root, "svc", "metrics_test.go"))
	writeSourceFile(t, filepath.Join(root, ".tmp", "TestOther123", "001", "metrics.go"))
	t.Setenv("TMPDIR", filepath.Join(root, ".tmp"))

	var seen []string
	if err := walkRepoGoSources(root, func(path string) error {
		rel, _ := filepath.Rel(root, path)
		seen = append(seen, rel)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if want := []string{filepath.Join("svc", "metrics.go")}; len(seen) != 1 || seen[0] != want[0] {
		t.Fatalf("walked %v, want %v", seen, want)
	}
}

func TestWalkRepoGoSourcesToleratesDirectoryRemovedDuringWalk(t *testing.T) {
	root := t.TempDir()
	writeSourceFile(t, filepath.Join(root, "a", "first.go"))
	writeSourceFile(t, filepath.Join(root, "b", "second.go"))
	writeSourceFile(t, filepath.Join(root, "c", "third.go"))

	var seen []string
	err := walkRepoGoSources(root, func(path string) error {
		rel, _ := filepath.Rel(root, path)
		seen = append(seen, rel)
		if rel == filepath.Join("a", "first.go") {
			// Another test package removing its fixture while this walk has
			// already listed the parent directory.
			return os.RemoveAll(filepath.Join(root, "b"))
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk failed on a directory removed mid-walk: %v", err)
	}
	sort.Strings(seen)
	want := []string{filepath.Join("a", "first.go"), filepath.Join("c", "third.go")}
	if len(seen) != len(want) || seen[0] != want[0] || seen[1] != want[1] {
		t.Fatalf("walked %v, want %v", seen, want)
	}
}

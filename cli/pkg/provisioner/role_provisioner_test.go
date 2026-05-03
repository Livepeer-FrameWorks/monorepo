package provisioner

import (
	"os"
	"path/filepath"
	"testing"
)

func TestAnsibleCollectionsPathPrefersRepoCollection(t *testing.T) {
	root := filepath.Join("repo", "ansible")
	cache := filepath.Join("cache", "collections")

	got := ansibleCollectionsPath(root, cache)
	want := filepath.Join(root, "collections") + string(os.PathListSeparator) + cache
	if got != want {
		t.Fatalf("ansibleCollectionsPath = %q, want %q", got, want)
	}
}

func TestAnsibleCollectionsPathAvoidsDuplicateLocalPath(t *testing.T) {
	root := filepath.Join("repo", "ansible")
	local := filepath.Join(root, "collections")

	if got := ansibleCollectionsPath(root, local); got != local {
		t.Fatalf("ansibleCollectionsPath duplicate local = %q, want %q", got, local)
	}
}

package ansiblerun

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The pinned collections declare older ansible-core ceilings than current
// releases ship; ansible-galaxy refuses them unless told to ignore the
// mismatch, which the release apply's install must do on an operator's
// up-to-date Ansible.
func TestGalaxyInstallIgnoresAnsibleVersionMismatch(t *testing.T) {
	dir := t.TempDir()
	envOut := filepath.Join(dir, "env")
	binary := filepath.Join(dir, "ansible-galaxy")
	script := "#!/bin/sh\nenv > " + envOut + "\n"
	if err := os.WriteFile(binary, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	requirements := filepath.Join(dir, "requirements.yml")
	if err := os.WriteFile(requirements, []byte("collections: []\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := runGalaxyCollectionInstall(context.Background(), binary, requirements, filepath.Join(dir, "cache")); err != nil {
		t.Fatalf("collection install: %v", err)
	}
	env, err := os.ReadFile(envOut)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(env), "ANSIBLE_COLLECTIONS_ON_ANSIBLE_VERSION_MISMATCH=ignore\n") {
		t.Fatalf("ansible-galaxy ran without ANSIBLE_COLLECTIONS_ON_ANSIBLE_VERSION_MISMATCH=ignore:\n%s", env)
	}
}

func TestHashFile_IsStable(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "requirements.yml")
	if err := os.WriteFile(path, []byte("collections: []\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	a, err := hashFile(path)
	if err != nil {
		t.Fatalf("hashFile: %v", err)
	}
	b, err := hashFile(path)
	if err != nil {
		t.Fatalf("hashFile: %v", err)
	}
	if a != b {
		t.Fatalf("hash unstable: %s vs %s", a, b)
	}
	if len(a) != 64 {
		t.Fatalf("hash length = %d, want 64 hex chars", len(a))
	}
}

func TestSentinelRoundTrip(t *testing.T) {
	dir := t.TempDir()

	ok, err := sentinelMatches(dir, "abc")
	if err != nil {
		t.Fatalf("sentinelMatches on empty cache: %v", err)
	}
	if ok {
		t.Fatal("empty cache should not match")
	}

	if writeErr := writeSentinel(dir, "abc"); writeErr != nil {
		t.Fatal(writeErr)
	}

	ok, err = sentinelMatches(dir, "abc")
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Fatal("sentinel should match after write")
	}

	ok, err = sentinelMatches(dir, "different")
	if err != nil {
		t.Fatal(err)
	}
	if ok {
		t.Fatal("sentinel should not match a different hash")
	}
}

func TestCollectionEnsurer_ResolveCacheDirRespectsOverride(t *testing.T) {
	override := t.TempDir()
	e := &CollectionEnsurer{CacheDir: override}
	got, err := e.resolveCacheDir()
	if err != nil {
		t.Fatal(err)
	}
	if got != override {
		t.Errorf("got %s, want %s", got, override)
	}
}

func TestInstallLockAcquireRelease(t *testing.T) {
	dir := t.TempDir()

	first, err := acquireInstallLock(dir)
	if err != nil {
		t.Fatalf("acquireInstallLock(first): %v", err)
	}
	releaseInstallLock(first)

	second, err := acquireInstallLock(dir)
	if err != nil {
		t.Fatalf("acquireInstallLock(second): %v", err)
	}
	releaseInstallLock(second)
}

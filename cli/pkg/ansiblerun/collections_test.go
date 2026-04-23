package ansiblerun

import (
	"os"
	"path/filepath"
	"testing"
)

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

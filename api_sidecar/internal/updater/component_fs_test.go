package updater

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestSortedKeys(t *testing.T) {
	got := sortedKeys(map[string]string{"c": "3", "a": "1", "b": "2"})
	if !reflect.DeepEqual(got, []string{"a", "b", "c"}) {
		t.Errorf("sortedKeys = %v, want sorted", got)
	}
	if got := sortedKeys(map[string]string{}); len(got) != 0 {
		t.Errorf("empty map = %v, want empty", got)
	}
	if got := sortedKeys(map[string]string{"only": "1"}); !reflect.DeepEqual(got, []string{"only"}) {
		t.Errorf("single = %v", got)
	}
}

func TestComponentInstallDir(t *testing.T) {
	// OS-specific root, but every variant ends in frameworks/<component>.
	got := componentInstallDir("mist")
	if !strings.HasSuffix(got, filepath.Join("frameworks", "mist")) {
		t.Errorf("componentInstallDir(mist) = %q, want .../frameworks/mist", got)
	}
}

// firstCommand walks (name,arg,arg,arg) quads and returns nil on the first that
// succeeds — the OS-specific restart-command fallback chain.
func TestFirstCommand(t *testing.T) {
	ctx := context.Background()
	if err := firstCommand(ctx, "true", "", "", ""); err != nil {
		t.Errorf("single succeeding command: %v", err)
	}
	if err := firstCommand(ctx, "false", "", "", "", "true", "", "", ""); err != nil {
		t.Errorf("fallback to second succeeding command: %v", err)
	}
	if err := firstCommand(ctx, "definitely-not-a-real-binary-xyz", "", "", ""); err == nil {
		t.Error("all-failing chain must return an error")
	}
	if err := firstCommand(ctx, "true", "only-three-fields"); err == nil {
		t.Error("a command list not divisible by 4 must error")
	}
}

// component-version file round-trips through the on-disk env file. The path
// probes production install dirs first, so we redirect HOME and skip if this
// machine actually has the production dirs (we never write to them).
func TestComponentVersionFileRoundTrip(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	want := filepath.Join(home, ".config/frameworks/component-versions.env")
	if componentVersionPath() != want {
		t.Skipf("machine has production component-version dirs; refusing to write there")
	}

	if err := WriteComponentVersion("helmsman", "v1.2.3"); err != nil {
		t.Fatal(err)
	}
	if err := WriteComponentVersion("mist", "2026-01-01"); err != nil {
		t.Fatal(err)
	}
	got := ReadComponentVersions()
	if got["HELMSMAN_VERSION"] != "v1.2.3" || got["MIST_VERSION"] != "2026-01-01" {
		t.Fatalf("round-trip = %v", got)
	}

	// Overwrite the same key in place.
	if err := WriteComponentVersion("helmsman", "v2.0.0"); err != nil {
		t.Fatal(err)
	}
	if v := ReadComponentVersions()["HELMSMAN_VERSION"]; v != "v2.0.0" {
		t.Fatalf("overwrite = %q, want v2.0.0", v)
	}

	// Rejections: unknown component, empty version, control characters.
	if err := WriteComponentVersion("bogus", "v1"); err == nil {
		t.Error("unknown component must be rejected")
	}
	if err := WriteComponentVersion("helmsman", "  "); err == nil {
		t.Error("empty version must be rejected")
	}
	if err := WriteComponentVersion("helmsman", "v1\n2"); err == nil {
		t.Error("version with control characters must be rejected")
	}
}

func TestInstallFilePreservesContentAndMode(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "src.bin")
	if err := os.WriteFile(src, []byte("payload"), 0o644); err != nil {
		t.Fatal(err)
	}
	dst := filepath.Join(dir, "nested", "dst.bin")
	if err := installFile(src, dst, 0o755); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(dst)
	if err != nil || string(b) != "payload" {
		t.Fatalf("installed content = %q (%v)", b, err)
	}
	info, _ := os.Stat(dst)
	if info.Mode().Perm() != 0o755 {
		t.Errorf("installed mode = %v, want 0755", info.Mode().Perm())
	}

	// Overwrites an existing destination.
	if err := os.WriteFile(src, []byte("updated"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := installFile(src, dst, 0o644); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(dst); string(b) != "updated" {
		t.Errorf("overwrite content = %q", b)
	}
}

func TestExtractTarGz(t *testing.T) {
	dir := t.TempDir()
	archivePath := filepath.Join(dir, "a.tar.gz")
	writeTarGz(t, archivePath, map[string]string{
		"bin/helmsman":  "binary",
		"etc/conf.yaml": "config",
	})

	dest := filepath.Join(dir, "out")
	if err := extractTarGz(archivePath, dest); err != nil {
		t.Fatal(err)
	}
	assertExtracted(t, dest, map[string]string{
		"bin/helmsman":  "binary",
		"etc/conf.yaml": "config",
	})
}

// safeJoin must reject archive members that would escape the destination, both
// directly through extractTarGz.
func TestExtractTarGzRejectsPathTraversal(t *testing.T) {
	dir := t.TempDir()
	archivePath := filepath.Join(dir, "evil.tar.gz")
	writeTarGz(t, archivePath, map[string]string{"../escape.sh": "pwned"})

	if err := extractTarGz(archivePath, filepath.Join(dir, "out")); err == nil {
		t.Fatal("expected extraction to reject a path-traversal member")
	}
	if _, err := os.Stat(filepath.Join(dir, "escape.sh")); !os.IsNotExist(err) {
		t.Fatal("traversal member must not be written outside the destination")
	}
}

func TestExtractZip(t *testing.T) {
	dir := t.TempDir()
	archivePath := filepath.Join(dir, "a.zip")
	writeZip(t, archivePath, map[string]string{
		"bin/caddy": "binary",
		"readme.md": "docs",
	})

	dest := filepath.Join(dir, "out")
	if err := extractZip(archivePath, dest); err != nil {
		t.Fatal(err)
	}
	assertExtracted(t, dest, map[string]string{
		"bin/caddy": "binary",
		"readme.md": "docs",
	})
}

// extractInto tries tar.gz, then zip, then falls back to a bare-file copy. The
// fallback is what lets a raw (uncompressed) binary artifact still install.
func TestExtractIntoBareFileFallback(t *testing.T) {
	dir := t.TempDir()
	raw := filepath.Join(dir, "helmsman")
	if err := os.WriteFile(raw, []byte("rawbinary"), 0o755); err != nil {
		t.Fatal(err)
	}
	dest := filepath.Join(dir, "out")
	if err := extractInto(raw, dest); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(filepath.Join(dest, "helmsman"))
	if err != nil || string(b) != "rawbinary" {
		t.Fatalf("bare-file fallback = %q (%v)", b, err)
	}
}

// executableFromArtifact extracts an archive and locates the component binary
// by name/prefix; a single-file artifact resolves unconditionally.
func TestExecutableFromArtifact(t *testing.T) {
	dir := t.TempDir()

	t.Run("locates binary by name", func(t *testing.T) {
		archivePath := filepath.Join(dir, "named.tar.gz")
		writeTarGz(t, archivePath, map[string]string{
			"dist/helmsman": "bin",
			"dist/README":   "docs",
		})
		path, cleanup, err := executableFromArtifact(archivePath, []string{"helmsman"}, "frameworks-helmsman-")
		if err != nil {
			t.Fatal(err)
		}
		defer cleanup()
		if filepath.Base(path) != "helmsman" {
			t.Errorf("found %q, want helmsman", path)
		}
	})

	t.Run("single-file artifact resolves even without a name match", func(t *testing.T) {
		archivePath := filepath.Join(dir, "single.tar.gz")
		writeTarGz(t, archivePath, map[string]string{"oddname": "bin"})
		path, cleanup, err := executableFromArtifact(archivePath, []string{"helmsman"}, "nope-")
		if err != nil {
			t.Fatal(err)
		}
		defer cleanup()
		if filepath.Base(path) != "oddname" {
			t.Errorf("found %q, want the lone file", path)
		}
	})

	t.Run("no match in a multi-file archive errors", func(t *testing.T) {
		archivePath := filepath.Join(dir, "multi.tar.gz")
		writeTarGz(t, archivePath, map[string]string{"a": "1", "b": "2"})
		_, cleanup, err := executableFromArtifact(archivePath, []string{"helmsman"}, "nope-")
		cleanup()
		if err == nil {
			t.Error("expected an error when no binary matches in a multi-file artifact")
		}
	})
}

// --- archive builders / assertions ---

func writeTarGz(t *testing.T, path string, files map[string]string) {
	t.Helper()
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	gz := gzip.NewWriter(f)
	defer gz.Close()
	tw := tar.NewWriter(gz)
	for name, content := range files {
		hdr := &tar.Header{Name: name, Mode: 0o644, Size: int64(len(content)), Typeflag: tar.TypeReg}
		if err := tw.WriteHeader(hdr); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write([]byte(content)); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
}

func writeZip(t *testing.T, path string, files map[string]string) {
	t.Helper()
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	zw := zip.NewWriter(f)
	for name, content := range files {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write([]byte(content)); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
}

func assertExtracted(t *testing.T, dest string, want map[string]string) {
	t.Helper()
	for rel, content := range want {
		b, err := os.ReadFile(filepath.Join(dest, rel))
		if err != nil {
			t.Errorf("missing %s: %v", rel, err)
			continue
		}
		if string(b) != content {
			t.Errorf("%s = %q, want %q", rel, b, content)
		}
	}
}

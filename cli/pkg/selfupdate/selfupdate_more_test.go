package selfupdate

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
)

func TestFileMode(t *testing.T) {
	t.Parallel()
	// mode 0 → default 0o755.
	if got := fileMode(0); got != 0o755 {
		t.Fatalf("fileMode(0)=%o, want 0755", got)
	}
	// non-zero preserved verbatim.
	for _, m := range []fs.FileMode{0o644, 0o600, 0o700, 0o755, 0o400} {
		if got := fileMode(m); got != m {
			t.Fatalf("fileMode(%o)=%o, want %o", m, got, m)
		}
	}
}

func TestExtractTarGz_RoundTrip(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	archive := filepath.Join(dir, "a.tar.gz")
	content := []byte("hello-binary")

	f, err := os.Create(archive)
	if err != nil {
		t.Fatal(err)
	}
	gz := gzip.NewWriter(f)
	tw := tar.NewWriter(gz)
	if err = tw.WriteHeader(&tar.Header{Name: "nested/dir/frameworks", Mode: 0o755, Size: int64(len(content))}); err != nil {
		t.Fatal(err)
	}
	if _, err = tw.Write(content); err != nil {
		t.Fatal(err)
	}
	_ = tw.Close()
	_ = gz.Close()
	_ = f.Close()

	dest := t.TempDir()
	if err = extractTarGz(archive, dest); err != nil {
		t.Fatalf("extractTarGz: %v", err)
	}
	// filepath.Base strips the nested dirs → frameworks lands at dest root.
	got, err := os.ReadFile(filepath.Join(dest, "frameworks"))
	if err != nil {
		t.Fatalf("read extracted: %v", err)
	}
	if string(got) != string(content) {
		t.Fatalf("content mismatch: %q", got)
	}
}

func TestExtractTarGz_TruncatedEntryErrors(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	archive := filepath.Join(dir, "bad.tar.gz")

	f, err := os.Create(archive)
	if err != nil {
		t.Fatal(err)
	}
	gz := gzip.NewWriter(f)
	tw := tar.NewWriter(gz)
	// Declare a larger Size than the bytes actually written → io.Copy in
	// extractTarGz fails (copyErr != nil must propagate).
	if err := tw.WriteHeader(&tar.Header{Name: "frameworks", Mode: 0o755, Size: 100}); err != nil {
		t.Fatal(err)
	}
	_, _ = tw.Write([]byte("short"))
	// Close will error because declared size != written; ignore and ship raw.
	_ = tw.Close()
	_ = gz.Close()
	_ = f.Close()

	if err := extractTarGz(archive, t.TempDir()); err == nil {
		t.Fatal("expected error from truncated tar entry copy")
	}
}

func TestExtractZip_RoundTripAndDirSkip(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	archive := filepath.Join(dir, "a.zip")
	content := []byte("zip-binary")

	f, err := os.Create(archive)
	if err != nil {
		t.Fatal(err)
	}
	zw := zip.NewWriter(f)
	// A directory entry must be skipped (IsDir branch).
	if _, err = zw.Create("somedir/"); err != nil {
		t.Fatal(err)
	}
	e, err := zw.Create("somedir/frameworks")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = e.Write(content); err != nil {
		t.Fatal(err)
	}
	_ = zw.Close()
	_ = f.Close()

	dest := t.TempDir()
	if err = extractZip(archive, dest); err != nil {
		t.Fatalf("extractZip: %v", err)
	}
	got, err := os.ReadFile(filepath.Join(dest, "frameworks"))
	if err != nil {
		t.Fatalf("read extracted: %v", err)
	}
	if string(got) != string(content) {
		t.Fatalf("content mismatch: %q", got)
	}
}

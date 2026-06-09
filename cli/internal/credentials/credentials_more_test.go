package credentials

import (
	"os"
	"path/filepath"
	"testing"
)

func TestFileStore_ParsesMalformedLines(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_DATA_HOME", dir)

	credDir := filepath.Join(dir, "frameworks")
	if err := os.MkdirAll(credDir, 0o700); err != nil {
		t.Fatal(err)
	}
	// =leadingeq → eq==0 (empty key) must be skipped.
	// noequals    → eq==-1 (no '=') must be skipped.
	// valid=ok    → eq>0, parsed.
	// k=          → eq>0 with empty value, parsed as empty string.
	content := "# header\n" +
		"=leadingeq\n" +
		"noequals\n" +
		"\n" +
		"   \n" +
		"valid=ok\n" +
		"k=\n"
	if err := os.WriteFile(filepath.Join(credDir, "credentials"), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}

	store := newFileStore()

	if got, err := store.Get("valid"); err != nil || got != "ok" {
		t.Fatalf("valid=ok must parse; got %q err %v", got, err)
	}
	if got, err := store.Get("k"); err != nil || got != "" {
		t.Fatalf("k= must parse to empty value; got %q err %v", got, err)
	}
	// The empty-key line (=leadingeq) must NOT create an entry under "".
	if got, err := store.Get(""); err != nil || got != "" {
		t.Fatalf("empty-key line must be skipped; got %q err %v", got, err)
	}
	// noequals must not become a key.
	if got, err := store.Get("noequals"); err != nil || got != "" {
		t.Fatalf("no-equals line must be skipped; got %q err %v", got, err)
	}
}

func TestFileStore_ValueWithEqualsSign(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_DATA_HOME", dir)
	credDir := filepath.Join(dir, "frameworks")
	if err := os.MkdirAll(credDir, 0o700); err != nil {
		t.Fatal(err)
	}
	// Only the FIRST '=' splits; the value may contain '='.
	if err := os.WriteFile(filepath.Join(credDir, "credentials"),
		[]byte("token=abc=def==\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	store := newFileStore()
	if got, err := store.Get("token"); err != nil || got != "abc=def==" {
		t.Fatalf("value after first '=' must be preserved; got %q err %v", got, err)
	}
}

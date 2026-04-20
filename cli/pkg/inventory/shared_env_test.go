package inventory

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadSharedEnvMergesFilesInOrder(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "a.env"), "KEY1=one\nKEY2=two\n")
	writeFile(t, filepath.Join(dir, "b.env"), "KEY2=override\nKEY3=three\n")

	m := &Manifest{EnvFiles: []string{"a.env", "b.env"}}
	env, err := LoadSharedEnv(m, dir, "")
	if err != nil {
		t.Fatalf("LoadSharedEnv: %v", err)
	}
	if env["KEY1"] != "one" {
		t.Errorf("KEY1 = %q, want %q", env["KEY1"], "one")
	}
	if env["KEY2"] != "override" {
		t.Errorf("KEY2 = %q, want %q (later file wins)", env["KEY2"], "override")
	}
	if env["KEY3"] != "three" {
		t.Errorf("KEY3 = %q, want %q", env["KEY3"], "three")
	}
}

func TestLoadSharedEnvSkipsCommentsAndBlanks(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "a.env"), "# leading comment\n\nFOO=bar\n# mid comment\nBAZ=qux\n")

	m := &Manifest{EnvFiles: []string{"a.env"}}
	env, err := LoadSharedEnv(m, dir, "")
	if err != nil {
		t.Fatalf("LoadSharedEnv: %v", err)
	}
	if env["FOO"] != "bar" || env["BAZ"] != "qux" {
		t.Errorf("unexpected env: %+v", env)
	}
	if len(env) != 2 {
		t.Errorf("expected 2 keys, got %d: %+v", len(env), env)
	}
}

func TestLoadSharedEnvRejectsAbsolutePath(t *testing.T) {
	m := &Manifest{EnvFiles: []string{"/etc/passwd"}}
	_, err := LoadSharedEnv(m, "/tmp", "")
	if err == nil {
		t.Fatal("expected error for absolute path")
	}
}

func TestLoadSharedEnvEmptyManifestFiles(t *testing.T) {
	m := &Manifest{}
	env, err := LoadSharedEnv(m, "", "")
	if err != nil {
		t.Fatalf("LoadSharedEnv: %v", err)
	}
	if len(env) != 0 {
		t.Errorf("expected empty map, got %+v", env)
	}
}

func TestLoadSharedEnvNilManifest(t *testing.T) {
	env, err := LoadSharedEnv(nil, "", "")
	if err != nil {
		t.Fatalf("LoadSharedEnv: %v", err)
	}
	if env == nil || len(env) != 0 {
		t.Errorf("expected empty non-nil map, got %v", env)
	}
}

func TestLoadSharedEnvDoesNotValidateKeys(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "only_one.env"), "RANDOM_KEY=x\n")

	m := &Manifest{EnvFiles: []string{"only_one.env"}}
	env, err := LoadSharedEnv(m, dir, "")
	if err != nil {
		t.Fatalf("generic loader must not care about specific keys: %v", err)
	}
	if _, ok := env["SERVICE_TOKEN"]; ok {
		t.Error("SERVICE_TOKEN should not be synthesized by the loader")
	}
	if env["RANDOM_KEY"] != "x" {
		t.Errorf("RANDOM_KEY = %q, want %q", env["RANDOM_KEY"], "x")
	}
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

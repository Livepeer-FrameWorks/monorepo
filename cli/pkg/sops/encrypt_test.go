package sops

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestEncryptFileInPlaceUsesFilenameOverride(t *testing.T) {
	dir := t.TempDir()
	binDir := filepath.Join(dir, "bin")
	if err := os.Mkdir(binDir, 0o755); err != nil {
		t.Fatal(err)
	}
	logPath := filepath.Join(dir, "args.log")
	fakeSops := filepath.Join(binDir, "sops")
	script := "#!/bin/sh\nfor arg in \"$@\"; do\n  printf '%s\\n' \"$arg\"\ndone > \"$SOPS_ARGS_LOG\"\n"
	if err := os.WriteFile(fakeSops, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("SOPS_ARGS_LOG", logPath)

	clusterDir := filepath.Join(dir, "clusters", "production")
	if err := os.MkdirAll(clusterDir, 0o755); err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(dir, ".sops.yaml")
	if err := os.WriteFile(configPath, []byte("creation_rules: []\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	tmpPath := filepath.Join(clusterDir, ".wg-edit-123.yaml")
	originalPath := filepath.Join(clusterDir, "hosts.enc.yaml")
	if err := EncryptFileInPlace(context.Background(), tmpPath, EncryptOptions{FilenameOverride: originalPath}); err != nil {
		t.Fatalf("encrypt: %v", err)
	}

	gotBytes, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	got := strings.Split(strings.TrimSpace(string(gotBytes)), "\n")
	want := []string{"--config", configPath, "--encrypt", "--in-place", "--filename-override", originalPath, tmpPath}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("unexpected sops args:\ngot:  %q\nwant: %q", got, want)
	}
}

func TestEncryptFileInPlacePassesAgeKeyFileInCommandEnv(t *testing.T) {
	dir := t.TempDir()
	binDir := filepath.Join(dir, "bin")
	if err := os.Mkdir(binDir, 0o755); err != nil {
		t.Fatal(err)
	}
	logPath := filepath.Join(dir, "age-key.log")
	fakeSops := filepath.Join(binDir, "sops")
	script := "#!/bin/sh\nprintf '%s\\n' \"$SOPS_AGE_KEY_FILE\" > \"$SOPS_AGE_KEY_LOG\"\n"
	if err := os.WriteFile(fakeSops, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	keyPath := filepath.Join(dir, "keys.txt")
	if err := os.WriteFile(keyPath, []byte("AGE-SECRET-KEY-test\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("SOPS_AGE_KEY_LOG", logPath)
	t.Setenv("SOPS_AGE_KEY_FILE", filepath.Join(dir, "wrong-key.txt"))

	if err := EncryptFileInPlace(context.Background(), filepath.Join(dir, "hosts.yaml"), EncryptOptions{AgeKeyFile: keyPath}); err != nil {
		t.Fatalf("encrypt: %v", err)
	}

	gotBytes, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	got := strings.TrimSpace(string(gotBytes))
	if got != keyPath {
		t.Fatalf("SOPS_AGE_KEY_FILE = %q, want %q", got, keyPath)
	}
	if env := os.Getenv("SOPS_AGE_KEY_FILE"); env != filepath.Join(dir, "wrong-key.txt") {
		t.Fatalf("process env was mutated: %q", env)
	}
}

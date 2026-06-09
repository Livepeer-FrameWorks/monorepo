package sops

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestEnvWith_ReplacesExistingKey(t *testing.T) {
	t.Parallel()
	env := []string{"A=1", "SOPS_AGE_KEY_FILE=/old", "B=2"}
	got := envWith(env, "SOPS_AGE_KEY_FILE", "/new")
	// Old entry removed, new appended exactly once.
	count := 0
	var val string
	for _, e := range got {
		if strings.HasPrefix(e, "SOPS_AGE_KEY_FILE=") {
			count++
			val = e
		}
	}
	if count != 1 {
		t.Fatalf("expected exactly one SOPS_AGE_KEY_FILE entry; got %d in %v", count, got)
	}
	if val != "SOPS_AGE_KEY_FILE=/new" {
		t.Fatalf("expected new value; got %q", val)
	}
	// Unrelated keys preserved.
	for _, want := range []string{"A=1", "B=2"} {
		found := false
		for _, e := range got {
			if e == want {
				found = true
			}
		}
		if !found {
			t.Fatalf("unrelated key %q dropped", want)
		}
	}
}

func TestFindConfigFile_ExtVsDir(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	sub := filepath.Join(root, "clusters", "prod")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	cfg := filepath.Join(root, ".sops.yaml")
	if err := os.WriteFile(cfg, []byte("creation_rules: []\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	// A path WITH an extension: dir is taken from filepath.Dir, walk upward finds cfg.
	filePath := filepath.Join(sub, "hosts.enc.yaml")
	got, ok := findConfigFile(filePath)
	if !ok || got != cfg {
		t.Fatalf("file-with-ext: got %q ok=%v, want %q", got, ok, cfg)
	}

	// A path WITHOUT an extension is treated as a directory itself.
	got2, ok2 := findConfigFile(sub)
	if !ok2 || got2 != cfg {
		t.Fatalf("dir-no-ext: got %q ok=%v, want %q", got2, ok2, cfg)
	}

	// Empty path → not found.
	if _, ok := findConfigFile(""); ok {
		t.Fatal("empty path must not find config")
	}
}

func TestEncryptFileInPlace_FilenameOverrideDrivesConfigLookup(t *testing.T) {
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

	// Two separate trees. .sops.yaml lives ONLY under the override tree.
	tmpTree := filepath.Join(dir, "tmpstage")
	overrideTree := filepath.Join(dir, "realtree", "clusters")
	if err := os.MkdirAll(tmpTree, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(overrideTree, 0o755); err != nil {
		t.Fatal(err)
	}
	cfg := filepath.Join(dir, "realtree", ".sops.yaml")
	if err := os.WriteFile(cfg, []byte("creation_rules: []\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	tmpPath := filepath.Join(tmpTree, ".wg-edit-1.yaml")
	overridePath := filepath.Join(overrideTree, "hosts.enc.yaml")
	if err := EncryptFileInPlace(context.Background(), tmpPath, EncryptOptions{FilenameOverride: overridePath}); err != nil {
		t.Fatalf("encrypt: %v", err)
	}

	raw, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	args := strings.Split(strings.TrimSpace(string(raw)), "\n")
	// Config lookup keyed off the override path must discover cfg.
	if args[0] != "--config" || args[1] != cfg {
		t.Fatalf("expected --config %s from override-tree lookup; got %v", cfg, args)
	}
}

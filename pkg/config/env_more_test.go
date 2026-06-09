package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sirupsen/logrus"
	"github.com/sirupsen/logrus/hooks/test"
)

func TestLoadEnv_LoadsPresentFile(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, ".env"), []byte("FW_LOADENV_PROBE=present\n"), 0o600); err != nil {
		t.Fatalf("write .env: %v", err)
	}
	t.Chdir(dir)
	t.Cleanup(func() { _ = os.Unsetenv("FW_LOADENV_PROBE") })

	logger, hook := test.NewNullLogger()
	logger.SetLevel(logrus.DebugLevel)
	LoadEnv(logger)

	if got := os.Getenv("FW_LOADENV_PROBE"); got != "present" {
		t.Fatalf("expected .env to be loaded (FW_LOADENV_PROBE=%q)", got)
	}
	// Present-file path emits the "Loaded env files" debug line, not the
	// "No local env files loaded" one.
	var sawLoaded bool
	for _, e := range hook.AllEntries() {
		if e.Message == "No local env files loaded; relying on process environment" {
			t.Fatalf("present file must not report no-files-loaded")
		}
		if strings.HasPrefix(e.Message, "Loaded env files") {
			sawLoaded = true
		}
	}
	if !sawLoaded {
		t.Fatal("expected 'Loaded env files' debug log when a file was loaded")
	}
}

func TestLoadEnv_NoFileLogsNoFilesLoaded(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)

	logger, hook := test.NewNullLogger()
	logger.SetLevel(logrus.DebugLevel)
	LoadEnv(logger)

	var sawNoFiles bool
	for _, e := range hook.AllEntries() {
		if e.Message == "No local env files loaded; relying on process environment" {
			sawNoFiles = true
		}
	}
	if !sawNoFiles {
		t.Fatal("expected 'No local env files loaded' debug log when no file present")
	}
}

func TestLoadEnv_NilLoggerNoFile(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	LoadEnv(nil)
}

func TestLoadEnv_NilLoggerWithFile(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, ".env"), []byte("FW_LOADENV_PROBE2=ok\n"), 0o600); err != nil {
		t.Fatalf("write .env: %v", err)
	}
	t.Chdir(dir)
	t.Cleanup(func() { _ = os.Unsetenv("FW_LOADENV_PROBE2") })
	LoadEnv(nil)
	if got := os.Getenv("FW_LOADENV_PROBE2"); got != "ok" {
		t.Fatalf("nil logger path must still load the file (got %q)", got)
	}
}

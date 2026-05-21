package config

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
)

// writeEnvFile creates a temp env file with the given content for a test.
// The cleanup function removes both the file and any keys the test installed
// from it.
func writeEnvFile(t *testing.T, name, content string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write env file: %v", err)
	}
	return path
}

func TestReloadFromFile_InitialAdditions(t *testing.T) {
	resetReloadStateForTest()
	t.Cleanup(resetReloadStateForTest)
	t.Cleanup(func() {
		_ = os.Unsetenv("TEST_RELOAD_FOO")
		_ = os.Unsetenv("TEST_RELOAD_BAR")
	})

	path := writeEnvFile(t, "foghorn.env", `TEST_RELOAD_FOO="first"
TEST_RELOAD_BAR="hello world"
`)

	res, err := ReloadFromFile(path)
	if err != nil {
		t.Fatalf("first reload: %v", err)
	}
	if !slices.Equal(res.Added, []string{"TEST_RELOAD_BAR", "TEST_RELOAD_FOO"}) {
		t.Errorf("Added: want [BAR FOO], got %v", res.Added)
	}
	if len(res.Changed) != 0 || len(res.Removed) != 0 {
		t.Errorf("expected only additions, got changed=%v removed=%v", res.Changed, res.Removed)
	}
	if got := os.Getenv("TEST_RELOAD_FOO"); got != "first" {
		t.Errorf("TEST_RELOAD_FOO = %q, want \"first\"", got)
	}
	if got := os.Getenv("TEST_RELOAD_BAR"); got != "hello world" {
		t.Errorf("TEST_RELOAD_BAR = %q, want \"hello world\"", got)
	}
}

func TestReloadFromFile_NoChangeIsEmptyResult(t *testing.T) {
	resetReloadStateForTest()
	t.Cleanup(resetReloadStateForTest)
	t.Cleanup(func() { _ = os.Unsetenv("TEST_RELOAD_STABLE") })

	path := writeEnvFile(t, "svc.env", `TEST_RELOAD_STABLE="same"
`)

	if _, err := ReloadFromFile(path); err != nil {
		t.Fatalf("first reload: %v", err)
	}
	res, err := ReloadFromFile(path)
	if err != nil {
		t.Fatalf("second reload: %v", err)
	}
	if !res.Empty() {
		t.Errorf("second reload should be empty, got added=%v changed=%v removed=%v",
			res.Added, res.Changed, res.Removed)
	}
}

func TestReloadFromFile_ChangedValue(t *testing.T) {
	resetReloadStateForTest()
	t.Cleanup(resetReloadStateForTest)
	t.Cleanup(func() { _ = os.Unsetenv("TEST_RELOAD_LEVEL") })

	dir := t.TempDir()
	path := filepath.Join(dir, "svc.env")
	if err := os.WriteFile(path, []byte(`TEST_RELOAD_LEVEL="info"`+"\n"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	if _, err := ReloadFromFile(path); err != nil {
		t.Fatalf("first reload: %v", err)
	}

	if err := os.WriteFile(path, []byte(`TEST_RELOAD_LEVEL="debug"`+"\n"), 0o600); err != nil {
		t.Fatalf("rewrite: %v", err)
	}
	res, err := ReloadFromFile(path)
	if err != nil {
		t.Fatalf("second reload: %v", err)
	}
	if !slices.Equal(res.Changed, []string{"TEST_RELOAD_LEVEL"}) {
		t.Errorf("Changed: want [TEST_RELOAD_LEVEL], got %v", res.Changed)
	}
	if got := os.Getenv("TEST_RELOAD_LEVEL"); got != "debug" {
		t.Errorf("TEST_RELOAD_LEVEL = %q, want \"debug\"", got)
	}
}

// The headline ownership-tracking property: a key that disappears between
// reloads is unset on the next reload. Critical so removing an entry from
// the env file in gitops actually clears the runtime env after `systemctl
// reload`.
func TestReloadFromFile_RemovedKeyIsUnset(t *testing.T) {
	resetReloadStateForTest()
	t.Cleanup(resetReloadStateForTest)
	t.Cleanup(func() { _ = os.Unsetenv("TEST_RELOAD_TEMP") })

	dir := t.TempDir()
	path := filepath.Join(dir, "svc.env")
	if err := os.WriteFile(path, []byte(`TEST_RELOAD_TEMP="here"`+"\n"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	if _, err := ReloadFromFile(path); err != nil {
		t.Fatalf("first reload: %v", err)
	}
	if os.Getenv("TEST_RELOAD_TEMP") != "here" {
		t.Fatal("first reload did not install TEST_RELOAD_TEMP")
	}

	if err := os.WriteFile(path, []byte("OTHER=present\n"), 0o600); err != nil {
		t.Fatalf("rewrite: %v", err)
	}
	t.Cleanup(func() { _ = os.Unsetenv("OTHER") })
	res, err := ReloadFromFile(path)
	if err != nil {
		t.Fatalf("second reload: %v", err)
	}
	if !slices.Contains(res.Removed, "TEST_RELOAD_TEMP") {
		t.Errorf("Removed: missing TEST_RELOAD_TEMP, got %v", res.Removed)
	}
	if _, present := os.LookupEnv("TEST_RELOAD_TEMP"); present {
		t.Error("TEST_RELOAD_TEMP still set after removal reload")
	}
}

func TestPrimeEnvFileOwnership_AllowsFirstReloadRemoval(t *testing.T) {
	resetReloadStateForTest()
	t.Cleanup(resetReloadStateForTest)
	t.Cleanup(func() { _ = os.Unsetenv("TEST_RELOAD_BOOT_ONLY") })

	dir := t.TempDir()
	path := filepath.Join(dir, "svc.env")
	if err := os.WriteFile(path, []byte(`TEST_RELOAD_BOOT_ONLY="boot"`+"\n"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := os.Setenv("TEST_RELOAD_BOOT_ONLY", "boot"); err != nil {
		t.Fatalf("setenv: %v", err)
	}
	if err := PrimeEnvFileOwnership(path); err != nil {
		t.Fatalf("prime: %v", err)
	}

	if err := os.WriteFile(path, []byte(""), 0o600); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	res, err := ReloadFromFile(path)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if !slices.Equal(res.Removed, []string{"TEST_RELOAD_BOOT_ONLY"}) {
		t.Fatalf("Removed got %v, want [TEST_RELOAD_BOOT_ONLY]", res.Removed)
	}
	if _, present := os.LookupEnv("TEST_RELOAD_BOOT_ONLY"); present {
		t.Fatal("boot-owned key still set after first reload")
	}
}

// The second headline property: we must never unset a key we did NOT
// install from an env file. If we did, a single reload could strip
// systemd-injected env (PATH, HOME, USER, ...) and kill the process.
func TestReloadFromFile_DoesNotUnsetUnownedKey(t *testing.T) {
	resetReloadStateForTest()
	t.Cleanup(resetReloadStateForTest)

	// Set a key OUTSIDE of any env file reload, then run a reload with a
	// completely different key. The unowned key must survive.
	if err := os.Setenv("TEST_UNOWNED_KEY", "preserve_me"); err != nil {
		t.Fatalf("seed unowned: %v", err)
	}
	t.Cleanup(func() { _ = os.Unsetenv("TEST_UNOWNED_KEY") })

	path := writeEnvFile(t, "svc.env", "TEST_OWNED_KEY=installed\n")
	t.Cleanup(func() { _ = os.Unsetenv("TEST_OWNED_KEY") })
	if _, err := ReloadFromFile(path); err != nil {
		t.Fatalf("reload: %v", err)
	}

	// Drop the owned key and reload; the unowned key must remain.
	if err := os.WriteFile(path, []byte(""), 0o600); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	if _, err := ReloadFromFile(path); err != nil {
		t.Fatalf("second reload: %v", err)
	}
	if got, present := os.LookupEnv("TEST_UNOWNED_KEY"); !present || got != "preserve_me" {
		t.Errorf("unowned key was touched: present=%v val=%q", present, got)
	}
}

func TestReloadFromFile_ParsesQuotedAndUnquoted(t *testing.T) {
	resetReloadStateForTest()
	t.Cleanup(resetReloadStateForTest)
	t.Cleanup(func() {
		for _, k := range []string{"TR_QUOTED", "TR_UNQUOTED", "TR_EMPTY", "TR_SPECIAL"} {
			_ = os.Unsetenv(k)
		}
	})

	path := writeEnvFile(t, "svc.env", `# leading comment
TR_QUOTED="quoted value"
TR_UNQUOTED=unquoted-value
TR_EMPTY=""

# blank lines and comments are fine
TR_SPECIAL="has \"escaped\" quotes"
`)
	res, err := ReloadFromFile(path)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if len(res.Added) != 4 {
		t.Errorf("expected 4 keys added, got %d: %v", len(res.Added), res.Added)
	}
	if got := os.Getenv("TR_QUOTED"); got != "quoted value" {
		t.Errorf("TR_QUOTED = %q", got)
	}
	if got := os.Getenv("TR_UNQUOTED"); got != "unquoted-value" {
		t.Errorf("TR_UNQUOTED = %q", got)
	}
	if got, present := os.LookupEnv("TR_EMPTY"); !present || got != "" {
		t.Errorf("TR_EMPTY: present=%v val=%q", present, got)
	}
	if got := os.Getenv("TR_SPECIAL"); got != `has "escaped" quotes` {
		t.Errorf("TR_SPECIAL = %q", got)
	}
}

func TestReloadFromFile_EmptyPath(t *testing.T) {
	resetReloadStateForTest()
	if _, err := ReloadFromFile(""); err == nil {
		t.Fatal("expected error for empty path")
	}
}

func TestReloadFromFile_MissingFile(t *testing.T) {
	resetReloadStateForTest()
	if _, err := ReloadFromFile(filepath.Join(t.TempDir(), "does-not-exist.env")); err == nil {
		t.Fatal("expected error for missing file")
	}
}

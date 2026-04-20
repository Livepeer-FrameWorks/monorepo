package credentials

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"testing"

	"frameworks/cli/internal/config"
)

// fakeEnv lets us inject env values without touching the real process env.
type fakeEnv map[string]string

func (e fakeEnv) Get(k string) string { return e[k] }

// TestResolve_EnvOverrideWins proves the core override semantics: when an
// env var is set, it always wins over the store regardless of what the
// store holds. This is the contract the tray's FW_USER_TOKEN injection
// (and CI-style FW_USER_TOKEN=xyz frameworks ...) relies on.
func TestResolve_EnvOverrideWins(t *testing.T) {
	store := newInMemoryStore()
	_ = store.Set(AccountUserSession, "from-store")

	got, err := resolveWith(store, fakeEnv{EnvUserToken: "from-env"}, AccountUserSession)
	if err != nil {
		t.Fatal(err)
	}
	if got != "from-env" {
		t.Errorf("env must win, got %q", got)
	}
}

// TestResolve_StoreWhenNoEnv falls through to the backing store when no
// env override is set.
func TestResolve_StoreWhenNoEnv(t *testing.T) {
	store := newInMemoryStore()
	_ = store.Set(AccountUserSession, "user")

	got, err := resolveWith(store, fakeEnv{}, AccountUserSession)
	if err != nil {
		t.Fatal(err)
	}
	if got != "user" {
		t.Errorf("want user, got %q", got)
	}
}

// TestResolve_MissingReturnsEmpty — both env and store empty → ("", nil).
// Callers decide whether missing creds are fatal; the store never invents
// a value.
func TestResolve_MissingReturnsEmpty(t *testing.T) {
	store := newInMemoryStore()
	got, err := resolveWith(store, fakeEnv{}, AccountUserSession)
	if err != nil {
		t.Fatal(err)
	}
	if got != "" {
		t.Errorf("want empty, got %q", got)
	}
}

// TestFileStore_RoundTripWithStrictPerms verifies the non-Darwin fallback
// honors its mode-0600 contract: written file exists, is readable, and has
// the expected permission bits.
func TestFileStore_RoundTripWithStrictPerms(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_DATA_HOME", dir)

	store := newFileStore()

	if err := store.Set("test_account", "hello"); err != nil {
		t.Fatal(err)
	}
	got, err := store.Get("test_account")
	if err != nil {
		t.Fatal(err)
	}
	if got != "hello" {
		t.Errorf("want hello, got %q", got)
	}

	// Contract: mode 0600. Anything looser is a security bug.
	path := filepath.Join(dir, "frameworks", "credentials")
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat %s: %v", path, err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Errorf("want 0600, got %o", info.Mode().Perm())
	}

	// Delete removes the entry without erroring on subsequent Get.
	if delErr := store.Delete("test_account"); delErr != nil {
		t.Fatal(delErr)
	}
	got, err = store.Get("test_account")
	if err != nil || got != "" {
		t.Errorf("want empty after delete, got %q err %v", got, err)
	}
}

// TestFileStore_MissingFileNotAnError — a fresh install has no credentials
// file; Get must not propagate "file not found" as an error.
func TestFileStore_MissingFileNotAnError(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_DATA_HOME", dir)

	store := newFileStore()
	got, err := store.Get(AccountUserSession)
	if err != nil {
		t.Errorf("missing file should yield empty+nil, got err %v", err)
	}
	if got != "" {
		t.Errorf("want empty, got %q", got)
	}
	// Assert the file really doesn't exist (sanity: we're testing the
	// "no file" branch, not silently creating one). errors.Is handles
	// fs.PathError wrapping correctly.
	_, statErr := os.Stat(filepath.Join(dir, "frameworks", "credentials"))
	if statErr == nil {
		t.Errorf("Get should not create the file")
	} else if !errors.Is(statErr, fs.ErrNotExist) {
		t.Errorf("expected ErrNotExist, got %v", statErr)
	}
}

// TestResolveUserAuth_HonorsInjectedEnv proves the resolver actually
// uses the env parameter it advertises (the previous implementation
// silently ignored it and read os.Getenv, which made the function
// untestable from outside the process env).
func TestResolveUserAuth_HonorsInjectedEnv(t *testing.T) {
	store := newInMemoryStore()
	_ = store.Set(AccountUserSession, "from-store")

	envFixture := config.MapEnv{
		EnvUserToken: "from-env",
	}

	jwt, err := ResolveUserAuth(envFixture, store)
	if err != nil {
		t.Fatal(err)
	}
	if jwt != "from-env" {
		t.Errorf("user-session env override ignored; got %q", jwt)
	}
}

// --- in-memory Store for tests ---------------------------------------------

type memStore struct{ m map[string]string }

func newInMemoryStore() *memStore { return &memStore{m: map[string]string{}} }

func (s *memStore) Name() string                 { return "memory" }
func (s *memStore) Get(k string) (string, error) { return s.m[k], nil }
func (s *memStore) Set(k, v string) error        { s.m[k] = v; return nil }
func (s *memStore) Delete(k string) error        { delete(s.m, k); return nil }

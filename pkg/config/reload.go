package config

import (
	"fmt"
	"os"
	"sort"
	"sync"

	"github.com/joho/godotenv"
)

// ReloadResult describes the changes a single ReloadFromFile call applied
// to the process environment. Added/Changed/Removed are disjoint sets of
// key names — handlers log them so operators can confirm a `systemctl
// reload` actually took effect.
type ReloadResult struct {
	Added   []string
	Changed []string
	Removed []string
}

// Empty reports whether the reload was a true no-op (file content matches
// what's already in the environment for every key the file owns).
func (r ReloadResult) Empty() bool {
	return len(r.Added) == 0 && len(r.Changed) == 0 && len(r.Removed) == 0
}

var (
	// envFileMu guards ownedKeys — the set of keys we've previously
	// installed from an env file. Tracking this is what lets us safely
	// Unsetenv keys that disappear between reloads without ever touching
	// systemd-injected env (PATH, HOME, USER, Environment= directives,
	// etc.) — we only unset keys we know we installed.
	envFileMu sync.Mutex
	ownedKeys = map[string]struct{}{}
)

// ReloadFromFile re-parses an env file (KEY="value" lines, the format
// the go_service Ansible role writes via copy+jinja) and applies any
// changes to the process environment via os.Setenv / os.Unsetenv.
//
// Behavior:
//   - Keys present in the file that differ from the current env value:
//     os.Setenv applied, recorded in Added (if previously unowned) or
//     Changed (if previously owned).
//   - Keys present in the env file on a previous reload but absent now:
//     os.Unsetenv applied, recorded in Removed. Only owned keys are
//     touched — systemd-injected env is never affected.
//   - Reloads are cumulative across calls: ownership of a key is
//     established on the first reload that installs it and persists
//     until the key is removed by a later reload.
//
// Safe to call concurrently from multiple SIGHUP handlers (services
// typically register one) — internal mutex serializes the diff+apply.
func ReloadFromFile(path string) (ReloadResult, error) {
	if path == "" {
		return ReloadResult{}, fmt.Errorf("reload env: empty path")
	}
	next, err := godotenv.Read(path)
	if err != nil {
		return ReloadResult{}, fmt.Errorf("reload env: read %s: %w", path, err)
	}

	envFileMu.Lock()
	defer envFileMu.Unlock()

	result := ReloadResult{}

	// Apply additions and changes.
	for key, value := range next {
		current := os.Getenv(key)
		_, owned := ownedKeys[key]
		if current == value && owned {
			continue
		}
		if err := os.Setenv(key, value); err != nil {
			return result, fmt.Errorf("reload env: setenv %s: %w", key, err)
		}
		if !owned {
			result.Added = append(result.Added, key)
			ownedKeys[key] = struct{}{}
		} else if current != value {
			result.Changed = append(result.Changed, key)
		}
	}

	// Unset keys we previously installed but that have been removed from
	// the file. Never touch a key we don't own.
	for key := range ownedKeys {
		if _, stillPresent := next[key]; stillPresent {
			continue
		}
		if err := os.Unsetenv(key); err != nil {
			return result, fmt.Errorf("reload env: unsetenv %s: %w", key, err)
		}
		result.Removed = append(result.Removed, key)
		delete(ownedKeys, key)
	}

	sort.Strings(result.Added)
	sort.Strings(result.Changed)
	sort.Strings(result.Removed)
	return result, nil
}

// PrimeEnvFileOwnership records the keys currently present in path without
// changing the process environment. Native services call this during startup
// after systemd has already injected EnvironmentFile values; the first
// SIGHUP can then unset a key that was removed from the file after boot.
func PrimeEnvFileOwnership(path string) error {
	if path == "" {
		return fmt.Errorf("prime env ownership: empty path")
	}
	current, err := godotenv.Read(path)
	if err != nil {
		return fmt.Errorf("prime env ownership: read %s: %w", path, err)
	}

	envFileMu.Lock()
	defer envFileMu.Unlock()
	for key := range current {
		ownedKeys[key] = struct{}{}
	}
	return nil
}

// resetReloadStateForTest clears the owned-keys snapshot. Tests use this
// so neighbouring cases don't share state via the package-level map.
func resetReloadStateForTest() {
	envFileMu.Lock()
	ownedKeys = map[string]struct{}{}
	envFileMu.Unlock()
}

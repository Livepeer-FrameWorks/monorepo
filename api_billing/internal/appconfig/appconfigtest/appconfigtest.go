// Package appconfigtest sets Purser runtime configuration values in tests.
package appconfigtest

import (
	"maps"
	"sync"
	"testing"

	"frameworks/api_billing/internal/appconfig"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/config"
)

var (
	mu       sync.Mutex
	values   = map[string]string{}
	declared = sync.OnceValue(func() map[string]bool {
		fields, err := config.Describe(&appconfig.PurserRuntime{}, config.Options{Lookup: func(string) (string, bool) { return "", false }})
		if err != nil {
			panic(err)
		}
		keys := make(map[string]bool, len(fields))
		for _, field := range fields {
			keys[field.Key] = true
		}
		return keys
	})
)

// Set installs a runtime configuration in which key has value, on top of the
// values earlier Set calls installed, and restores the previous configuration
// when the test ends. An empty value counts as unset, so the declared default
// applies. Like t.Setenv, it changes process-wide state and must not be used
// in parallel tests.
func Set(t testing.TB, key, value string) {
	t.Helper()
	if !declared()[key] {
		t.Fatalf("appconfigtest: %s is not a PurserRuntime key", key)
	}

	mu.Lock()
	defer mu.Unlock()
	previous := values
	next := maps.Clone(previous)
	next[key] = value
	opts := config.Options{
		Service: "purser",
		Lookup: func(k string) (string, bool) {
			v, ok := next[k]
			return v, ok
		},
	}
	cfg, err := config.Load[appconfig.PurserRuntime](opts)
	if err != nil {
		t.Fatalf("appconfigtest: set %s: %v", key, err)
	}
	values = next
	previousLive := appconfig.InstallRuntime(config.NewLive(cfg, opts))
	t.Cleanup(func() {
		mu.Lock()
		defer mu.Unlock()
		values = previous
		appconfig.InstallRuntime(previousLive)
	})
}

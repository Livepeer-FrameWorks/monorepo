// Package appconfigtest installs Helmsman configurations for package tests.
// The accessors in appconfig read only an installed configuration, as the
// serve process installs one before starting any component, so a test sets a
// Helmsman key here instead of in the process environment.
package appconfigtest

import (
	"maps"
	"os"
	"sync"
	"testing"

	"frameworks/api_sidecar/internal/appconfig"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/config"
)

var (
	mu sync.Mutex
	// values are the keys behind the configuration this package installed
	// last; nil when it installed none.
	values map[string]string
)

// requiredValues fills every required Helmsman key.
func requiredValues() map[string]string {
	return map[string]string{
		"NODE_ID":              "edge-test",
		"FOGHORN_CONTROL_ADDR": "foghorn:18019",
		"MISTSERVER_URL":       "http://localhost:4242",
		"EDGE_PUBLIC_URL":      "https://edge.test/view",
		"HELMSMAN_STATE_DIR":   "/tmp/helmsman-test-state",
	}
}

func load(from map[string]string) (*config.Live[appconfig.Helmsman], error) {
	opts := config.Options{Service: appconfig.ServiceID, Lookup: func(key string) (string, bool) {
		value, ok := from[key]
		return value, ok
	}}
	cfg, err := config.Load[appconfig.Helmsman](opts)
	if err != nil {
		return nil, err
	}
	return config.NewLive(cfg, opts), nil
}

// Main installs a configuration with only the required keys set, runs the
// package's tests, and exits with their status. A package whose code reads
// the Helmsman configuration calls it from TestMain.
func Main(m *testing.M) {
	mu.Lock()
	values = requiredValues()
	live, err := load(values)
	mu.Unlock()
	if err != nil {
		panic("appconfigtest: " + err.Error())
	}
	appconfig.Install(live)
	os.Exit(m.Run())
}

// Setenv sets one Helmsman key on top of the configuration installed now and
// installs the result until the test ends, like t.Setenv for the process
// environment. An empty value unsets the key, so its default applies. Like
// t.Setenv, it must not be used in parallel tests.
func Setenv(t testing.TB, key, value string) {
	t.Helper()
	mu.Lock()
	defer mu.Unlock()
	next := maps.Clone(values)
	if next == nil {
		next = requiredValues()
	}
	next[key] = value
	live, err := load(next)
	if err != nil {
		t.Fatalf("load test Helmsman configuration: %v", err)
	}
	previousValues := values
	previous := appconfig.Install(live)
	values = next
	t.Cleanup(func() {
		mu.Lock()
		defer mu.Unlock()
		appconfig.Install(previous)
		values = previousValues
	})
}

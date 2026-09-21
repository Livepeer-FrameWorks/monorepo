package control

import (
	"testing"

	"frameworks/api_balancing/internal/appconfig"
)

// useFoghornConfig makes appconfig.Current return settings until the test
// ends. It returns settings so a test can change values mid-test, the way a
// SIGHUP reload replaces them.
func useFoghornConfig(t *testing.T, settings *appconfig.Foghorn) *appconfig.Foghorn {
	t.Helper()
	t.Cleanup(appconfig.Install(func() *appconfig.Foghorn { return settings }))
	return settings
}

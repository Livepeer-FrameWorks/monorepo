package triggers

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

// livepeerVODSettings returns a configuration with the given raw
// LIVEPEER_VOD_DEADLINE_MS and LIVEPEER_VOD_MIN_SPEED values.
func livepeerVODSettings(deadlineMS, minSpeed string) *appconfig.Foghorn {
	settings := &appconfig.Foghorn{}
	settings.LivepeerVODDeadlineMS = deadlineMS
	settings.LivepeerVODMinSpeed = minSpeed
	return settings
}

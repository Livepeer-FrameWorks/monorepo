package config

import (
	"testing"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/mist"
)

func TestApplyBaselineMistConfigIncludesOperationalModes(t *testing.T) {
	desired := map[string]any{}

	applyBaselineMistConfig(desired)

	want := map[string]any{
		"accesslog":              "LOG",
		"debug":                  4,
		"prometheus":             mist.MetricsConfigValue,
		"sessionInputMode":       15,
		"sessionOutputMode":      15,
		"sessionStreamInfoMode":  "1",
		"sessionUnspecifiedMode": 0,
		"sessionViewerMode":      14,
		"tknMode":                15,
		"trustedproxy":           []string{"127.0.0.1", "::1", "localhost", "nginx"},
	}

	for key, wantValue := range want {
		if !protocolValuesEqual(desired[key], wantValue) {
			t.Fatalf("%s = %#v, want %#v", key, desired[key], wantValue)
		}
	}
}

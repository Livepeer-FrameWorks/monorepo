package config

import (
	"testing"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
)

func TestDriftCheckRecordsConfiguredListenerPorts(t *testing.T) {
	t.Cleanup(func() { mistListenerPorts.Store(nil) })
	mist := &recordingMistAPI{backupResult: map[string]interface{}{"config": map[string]interface{}{
		"triggers": desiredTriggers(),
		"protocols": []any{
			map[string]any{"connector": "TSSRT", "port": float64(8889)},
			map[string]any{"connector": "RTMP", "port": "1936"},
			map[string]any{"connector": "HTTP", "port": float64(8080), "pubaddr": []any{"https://edge.example/view/"}},
			map[string]any{"connector": "HLS"},
		},
	}}}
	m := &Manager{mistClient: mist, logger: logging.NewLogger()}
	m.repairTriggerDefinitions()

	if got := MistListenerPort("TSSRT"); got != 8889 {
		t.Fatalf("TSSRT port = %d, want the configured 8889", got)
	}
	if got := MistListenerPort("rtmp"); got != 1936 {
		t.Fatalf("RTMP port = %d, want 1936", got)
	}
	if got := MistListenerPort("HTTP"); got != 0 {
		t.Fatalf("HTTP port = %d; a public address override decides what clients dial", got)
	}
	if got := MistListenerPort("HLS"); got != 0 {
		t.Fatalf("HLS port = %d, want unknown", got)
	}

	mist.backupResult = map[string]interface{}{"config": map[string]interface{}{"triggers": desiredTriggers()}}
	m.repairTriggerDefinitions()
	if got := MistListenerPort("TSSRT"); got != 0 {
		t.Fatalf("TSSRT port = %d after the connector left Mist's config", got)
	}
}

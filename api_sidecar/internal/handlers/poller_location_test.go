package handlers

import (
	"testing"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	logrustest "github.com/sirupsen/logrus/hooks/test"
)

// Mist repeats the node's location, or its absence, in every metrics report.
// Only a change is logged: a missing location once until it appears, and new
// coordinates once.
func TestMistLocationLoggedOnlyOnChange(t *testing.T) {
	previous := monitorLogger
	monitorLogger = logging.NewLogger()
	t.Cleanup(func() { monitorLogger = previous })
	logs := logrustest.NewLocal(monitorLogger)
	pm := &PrometheusMonitor{}
	count := func(message string) int {
		n := 0
		for _, entry := range logs.AllEntries() {
			if entry.Message == message {
				n++
			}
		}
		return n
	}
	withLoc := map[string]any{"loc": map[string]any{"lat": 52.1, "lon": 4.3, "name": "Leiden"}}
	withoutLoc := map[string]any{"cpu": float64(1)}

	for range 5 {
		pm.applyMistLocationLocked("node", withoutLoc)
	}
	if got := count("No location data from MistServer for node"); got != 1 {
		t.Fatalf("missing location logged %d times over five reports, want once", got)
	}
	for range 5 {
		pm.applyMistLocationLocked("node", withLoc)
	}
	if got := count("Updated PrometheusMonitor location data"); got != 1 {
		t.Fatalf("unchanged location logged %d times, want once", got)
	}
	if pm.location != "Leiden" || pm.latitude == nil || *pm.latitude != 52.1 {
		t.Fatalf("location not applied: %q %v", pm.location, pm.latitude)
	}
	pm.applyMistLocationLocked("node", withoutLoc)
	if got := count("No location data from MistServer for node"); got != 2 {
		t.Fatalf("location disappearing again logged %d times in total, want 2", got)
	}
}

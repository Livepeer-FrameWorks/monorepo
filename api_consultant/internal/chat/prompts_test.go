package chat

import (
	"strings"
	"testing"
)

// A streamer who wants to go live from a browser is sent to the dashboard
// studio and its guide; the SDK packages are for building ingest into an app.
func TestSystemPromptRoutesBrowserGoLiveToTheDashboardStudio(t *testing.T) {
	for _, want := range []string{
		"https://chartroom.frameworks.network/go-live",
		"/go-live?stream=<stream id>",
		"https://logbook.frameworks.network/builders/go-live-browser/",
		"Go Live in the dashboard for browser ingest",
	} {
		if !strings.Contains(SystemPrompt, want) {
			t.Errorf("system prompt does not contain %q", want)
		}
	}
}

package prompts

import (
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// An agent planning long-running work must know a spent balance keeps running
// sessions but refuses new ones, and where sessions stop.
func TestCapabilitiesPromptStatesTheSpentBalanceRule(t *testing.T) {
	result, err := handleCapabilitiesPrompt()
	if err != nil {
		t.Fatal(err)
	}
	text := result.Messages[0].Content.(*mcp.TextContent).Text
	for _, want := range []string{"At €0 or below, running streams and their viewers continue", "new viewers and reconnects are refused", "Below -€10, running sessions stop"} {
		if !strings.Contains(text, want) {
			t.Fatalf("capabilities prompt does not state %q", want)
		}
	}
	if strings.Contains(text, "Streams will fail if balance depletes") {
		t.Fatal("capabilities prompt still says streams fail when the balance depletes")
	}
}

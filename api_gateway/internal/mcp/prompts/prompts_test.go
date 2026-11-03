package prompts

import (
	"context"
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

// Both cost prompts must name the plan-change tool, or an agent asked to cut
// cost has no route to a cheaper plan.
func TestCostPromptsNameTheBillingTierTool(t *testing.T) {
	optimize, err := handleOptimizeCostsPrompt(context.Background(), nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	instructions, err := handleAgentInstructionsPrompt()
	if err != nil {
		t.Fatal(err)
	}
	for name, result := range map[string]*mcp.GetPromptResult{"optimize_costs": optimize, "agent_instructions": instructions} {
		text := result.Messages[0].Content.(*mcp.TextContent).Text
		if !strings.Contains(text, "`change_billing_tier`") {
			t.Fatalf("%s prompt does not name `change_billing_tier`", name)
		}
	}
}

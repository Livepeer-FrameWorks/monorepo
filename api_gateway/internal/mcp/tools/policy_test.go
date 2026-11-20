package tools

import (
	"testing"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/accesspolicy"
)

func TestToolSecurityAndAccessPolicyCatalogsMatch(t *testing.T) {
	security := ToolPolicies()
	access := accesspolicy.MCPToolClasses()
	for name, policy := range security {
		class, ok := access[name]
		if !ok {
			t.Errorf("MCP tool %q has security metadata but no access class", name)
			continue
		}
		if policy.AccessClass != class {
			t.Errorf("MCP tool %q access class = %q, want %q", name, policy.AccessClass, class)
		}
	}
	for name := range access {
		if _, ok := security[name]; !ok {
			t.Errorf("access catalog refers to unregistered MCP tool %q", name)
		}
	}
}

func TestAddToolPoliciesRejectsDuplicateTool(t *testing.T) {
	policies := make(map[string]ToolPolicy)
	addToolPolicies(policies, "streams:read", ToolRiskRead, "list_push_targets")
	defer func() {
		if recover() == nil {
			t.Fatal("duplicate MCP tool policy did not panic")
		}
	}()
	addToolPolicies(policies, "streams:write", ToolRiskWrite, "list_push_targets")
}

func TestDisconnectIngestDeclaresDestructiveConfirmation(t *testing.T) {
	policy, ok := ToolPolicyForName("disconnect_ingest")
	if !ok || !policy.Destructive || policy.Idempotent {
		t.Fatalf("disconnect metadata allows unsafe automatic replay: %+v", policy)
	}
	confirmation := toolEnums["disconnect_ingest"]["confirm"]
	if len(confirmation) != 1 || confirmation[0] != "DISCONNECT INGEST" {
		t.Fatalf("disconnect confirmation is not explicit: %v", confirmation)
	}
}

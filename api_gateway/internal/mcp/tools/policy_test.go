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

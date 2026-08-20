package accesspolicy

import "testing"

func TestCatalogClassesAreValid(t *testing.T) {
	valid := map[Class]bool{
		Authentication: true, Read: true, Control: true, Rated: true,
		PaymentRecovery: true, Privileged: true,
	}
	for catalogName, catalog := range map[string]map[string]Class{
		"graphql": graphqlMutationClasses,
		"mcp":     mcpToolClasses,
	} {
		for name, class := range catalog {
			if name == "" || !valid[class] {
				t.Errorf("%s operation %q has invalid class %q", catalogName, name, class)
			}
		}
	}
}

func TestOnlyRatedOperationsRequireFunds(t *testing.T) {
	for _, class := range []Class{Authentication, Read, Control, PaymentRecovery, Privileged} {
		if !class.UnfundedAllowed() {
			t.Errorf("class %q unexpectedly requires prepaid funds", class)
		}
	}
	if Rated.UnfundedAllowed() {
		t.Fatal("rated operations must require prepaid funds")
	}
}

func TestRepresentativeParity(t *testing.T) {
	tests := []struct {
		graphql string
		mcp     string
		want    Class
	}{
		{graphql: "createStream", mcp: "create_stream", want: Control},
		{graphql: "createClip", mcp: "create_clip", want: Rated},
		{graphql: "startDVR", mcp: "start_dvr", want: Rated},
		{graphql: "deleteStream", mcp: "delete_stream", want: Control},
		{graphql: "updateBillingDetails", mcp: "update_billing_details", want: PaymentRecovery},
	}
	for _, tt := range tests {
		graphqlClass, graphqlOK := GraphQLMutationClass(tt.graphql)
		mcpClass, mcpOK := MCPToolClass(tt.mcp)
		if !graphqlOK || !mcpOK || graphqlClass != tt.want || mcpClass != tt.want {
			t.Errorf("%s/%s = %q/%q (%v/%v), want %q", tt.graphql, tt.mcp, graphqlClass, mcpClass, graphqlOK, mcpOK, tt.want)
		}
	}
}

func TestEveryRatedMutationHasExplicitX402ExecutionStrategy(t *testing.T) {
	valid := map[X402MutationStrategy]bool{X402OwnerIdempotency: true, X402Unsupported: true}
	for name, class := range graphqlMutationClasses {
		if class != Rated {
			continue
		}
		strategy, ok := GraphQLX402MutationStrategy(name)
		if !ok || !valid[strategy] {
			t.Errorf("rated GraphQL mutation %q has no explicit x402 owner strategy", name)
		}
	}
	for name, class := range mcpToolClasses {
		if class != Rated {
			continue
		}
		strategy, ok := MCPX402MutationStrategy(name)
		if !ok || !valid[strategy] {
			t.Errorf("rated MCP tool %q has no explicit x402 owner strategy", name)
		}
	}
}

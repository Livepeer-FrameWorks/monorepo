package control

import "testing"

func TestProcessingTenantEchoProtocolDeclared(t *testing.T) {
	if controlProtocolVersion < 13 {
		t.Fatalf("processing results echo tenants but registration advertises protocol %d", controlProtocolVersion)
	}
}

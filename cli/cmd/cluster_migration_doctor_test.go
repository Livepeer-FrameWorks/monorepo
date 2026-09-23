package cmd

import (
	"strings"
	"testing"
)

func TestContractPendingMessageNamesRollbackWindowAndCommand(t *testing.T) {
	msg := contractPendingMessage("v0.3.11", 3)
	for _, want := range []string{
		"3 contract migration(s) not run yet",
		"rollback window",
		"frameworks cluster migrate --phase contract --to-version v0.3.11 --backup <fresh backup> --yes",
	} {
		if !strings.Contains(msg, want) {
			t.Fatalf("message %q missing %q", msg, want)
		}
	}
}

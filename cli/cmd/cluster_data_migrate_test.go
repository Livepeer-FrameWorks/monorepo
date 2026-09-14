package cmd

import (
	"bytes"
	"testing"

	"frameworks/cli/pkg/ssh"

	"github.com/spf13/cobra"
)

func TestWriteDataMigrateResultPreservesRemoteDiagnostics(t *testing.T) {
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	cmd := &cobra.Command{}
	cmd.SetOut(&stdout)
	cmd.SetErr(&stderr)

	writeDataMigrateResult(cmd, &ssh.CommandResult{
		Stdout: "migration progress",
		Stderr: "verification failed",
	})

	if got := stdout.String(); got != "migration progress\n" {
		t.Fatalf("stdout = %q", got)
	}
	if got := stderr.String(); got != "verification failed\n" {
		t.Fatalf("stderr = %q", got)
	}
}

func TestWriteDataMigrateResultAcceptsMissingSSHResult(t *testing.T) {
	writeDataMigrateResult(&cobra.Command{}, nil)
}
